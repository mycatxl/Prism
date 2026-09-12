// Prism Management Console

const app = {
    currentPage: 'dashboard',
    adminToken: '',

    init() {
        this.loadToken();
        this.setupNavigation();
        this.setupHashRouting();
        this.loadDashboard();
        this.startPolling();
    },

    loadToken() {
        this.adminToken = localStorage.getItem('prism_admin_token') || '';
        if (!this.adminToken) {
            const token = prompt('Enter admin token:');
            if (token) {
                this.adminToken = token;
                localStorage.setItem('prism_admin_token', token);
            }
        }
    },

    setupNavigation() {
        document.querySelectorAll('.nav-link').forEach(link => {
            link.addEventListener('click', (e) => {
                e.preventDefault();
                const page = e.target.dataset.page;
                this.navigate(page);
            });
        });
    },

    setupHashRouting() {
        window.addEventListener('hashchange', () => {
            const page = location.hash.slice(1) || 'dashboard';
            this.navigate(page);
        });

        const initialPage = location.hash.slice(1) || 'dashboard';
        this.navigate(initialPage);
    },

    navigate(page) {
        document.querySelectorAll('.nav-link').forEach(link => {
            link.classList.toggle('active', link.dataset.page === page);
        });

        document.querySelectorAll('.page').forEach(p => {
            p.classList.toggle('active', p.id === `page-${page}`);
        });

        this.currentPage = page;
        location.hash = page;

        switch(page) {
            case 'dashboard':
                this.loadDashboard();
                break;
            case 'platforms':
                this.loadPlatforms();
                break;
            case 'endpoints':
                this.loadEndpoints();
                break;
            case 'leases':
                this.loadLeases();
                break;
            case 'metrics':
                this.loadMetrics();
                break;
            case 'logs':
                this.loadLogs();
                break;
        }
    },

    async api(path, options = {}) {
        const headers = {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${this.adminToken}`,
            ...options.headers
        };

        const response = await fetch(`/api${path}`, {
            ...options,
            headers
        });

        if (response.status === 401) {
            localStorage.removeItem('prism_admin_token');
            this.adminToken = '';
            this.loadToken();
            throw new Error('Authentication failed');
        }

        if (!response.ok) {
            const text = await response.text();
            throw new Error(text || `HTTP ${response.status}`);
        }

        if (response.status === 204) {
            return null;
        }

        return response.json();
    },

    async loadDashboard() {
        try {
            const [systemInfo, platforms] = await Promise.all([
                this.api('/v1/system/info'),
                this.api('/v1/platforms')
            ]);

            document.getElementById('system-info').innerHTML = `
                <div class="stat">
                    <div class="stat-label">Version</div>
                    <div class="stat-value">${systemInfo.version || 'N/A'}</div>
                </div>
                <div class="stat" style="margin-top: 16px;">
                    <div class="stat-label">Uptime</div>
                    <div class="stat-value">${this.formatDuration(systemInfo.uptime_seconds || 0)}</div>
                </div>
            `;

            const platformCount = platforms?.platforms?.length || 0;
            document.getElementById('platform-summary').innerHTML = `
                <div class="stat">
                    <div class="stat-label">Total Platforms</div>
                    <div class="stat-value">${platformCount}</div>
                </div>
            `;

            let totalLeases = 0;
            if (platforms?.platforms) {
                for (const platform of platforms.platforms) {
                    try {
                        const leases = await this.api(`/v1/platforms/${platform.id}/leases`);
                        totalLeases += leases?.leases?.length || 0;
                    } catch (e) {
                        console.error(`Failed to load leases for ${platform.id}:`, e);
                    }
                }
            }

            document.getElementById('active-leases').innerHTML = `
                <div class="stat">
                    <div class="stat-label">Active Leases</div>
                    <div class="stat-value">${totalLeases}</div>
                </div>
            `;
        } catch (error) {
            console.error('Dashboard load error:', error);
            this.showToast(`Failed to load dashboard: ${error.message}`, 'error');
        }
    },

    async loadPlatforms() {
        try {
            const data = await this.api('/v1/platforms');
            const platforms = data?.platforms || [];

            if (platforms.length === 0) {
                document.getElementById('platforms-list').innerHTML = `
                    <div style="padding: 48px; text-align: center; color: var(--text-secondary);">
                        No platforms configured
                    </div>
                `;
                return;
            }

            const rows = platforms.map(p => `
                <tr>
                    <td><strong>${this.escapeHtml(p.name)}</strong><br><code class="code">${this.escapeHtml(p.id)}</code></td>
                    <td>${p.node_count || 0} nodes</td>
                    <td>
                        ${p.sticky_enabled ? '<span class="badge badge-success">Sticky</span>' : ''}
                        ${p.scheduled_rotation_enabled ? '<span class="badge badge-info">Rotation</span>' : ''}
                    </td>
                    <td>
                        <button class="btn btn-secondary" onclick="app.editPlatform('${this.escapeHtml(p.id)}')">Edit</button>
                        <button class="btn btn-danger" onclick="app.deletePlatform('${this.escapeHtml(p.id)}')">Delete</button>
                    </td>
                </tr>
            `).join('');

            document.getElementById('platforms-list').innerHTML = `
                <table class="table">
                    <thead>
                        <tr>
                            <th>Platform</th>
                            <th>Nodes</th>
                            <th>Features</th>
                            <th>Actions</th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            `;
        } catch (error) {
            console.error('Platforms load error:', error);
            this.showToast(`Failed to load platforms: ${error.message}`, 'error');
        }
    },

    async loadEndpoints() {
        try {
            const data = await this.api('/v1/endpoints');
            const endpoints = data?.endpoints || [];

            const rows = endpoints.map(e => `
                <tr>
                    <td><strong>${this.escapeHtml(e.id)}</strong></td>
                    <td>${e.port}</td>
                    <td>${this.escapeHtml(e.platform_id || 'N/A')}</td>
                    <td>${e.enabled ? '<span class="badge badge-success">Enabled</span>' : '<span class="badge badge-error">Disabled</span>'}</td>
                    <td>
                        <button class="btn btn-secondary" onclick="app.editEndpoint('${this.escapeHtml(e.id)}')">Edit</button>
                        <button class="btn btn-danger" onclick="app.deleteEndpoint('${this.escapeHtml(e.id)}')">Delete</button>
                    </td>
                </tr>
            `).join('');

            document.getElementById('endpoints-list').innerHTML = `
                <table class="table">
                    <thead>
                        <tr>
                            <th>ID</th>
                            <th>Port</th>
                            <th>Platform</th>
                            <th>Status</th>
                            <th>Actions</th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            `;
        } catch (error) {
            console.error('Endpoints load error:', error);
            this.showToast(`Failed to load endpoints: ${error.message}`, 'error');
        }
    },

    async loadLeases() {
        try {
            const platformsData = await this.api('/v1/platforms');
            const platforms = platformsData?.platforms || [];

            let allLeases = [];
            for (const platform of platforms) {
                try {
                    const leasesData = await this.api(`/v1/platforms/${platform.id}/leases`);
                    const leases = leasesData?.leases || [];
                    allLeases.push(...leases.map(l => ({ ...l, platform_id: platform.id, platform_name: platform.name })));
                } catch (e) {
                    console.error(`Failed to load leases for ${platform.id}:`, e);
                }
            }

            if (allLeases.length === 0) {
                document.getElementById('leases-list').innerHTML = `
                    <div style="padding: 48px; text-align: center; color: var(--text-secondary);">
                        No active leases
                    </div>
                `;
                return;
            }

            const rows = allLeases.map(l => `
                <tr>
                    <td><code class="code">${this.escapeHtml(l.account)}</code></td>
                    <td>${this.escapeHtml(l.platform_name)}</td>
                    <td><code class="code">${this.escapeHtml(l.egress_ip)}</code></td>
                    <td>${this.formatTimestamp(l.created_at_ns)}</td>
                    <td>${this.formatTimestamp(l.expiry_ns)}</td>
                    <td>
                        <button class="btn btn-danger" onclick="app.deleteLease('${this.escapeHtml(l.platform_id)}', '${this.escapeHtml(l.account)}')">Delete</button>
                    </td>
                </tr>
            `).join('');

            document.getElementById('leases-list').innerHTML = `
                <table class="table">
                    <thead>
                        <tr>
                            <th>Account</th>
                            <th>Platform</th>
                            <th>IP</th>
                            <th>Created</th>
                            <th>Expires</th>
                            <th>Actions</th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            `;
        } catch (error) {
            console.error('Leases load error:', error);
            this.showToast(`Failed to load leases: ${error.message}`, 'error');
        }
    },

    async loadMetrics() {
        document.getElementById('metrics-content').innerHTML = `
            <div class="card">
                <div class="card-header">Metrics</div>
                <div class="card-body">
                    <p style="color: var(--text-secondary);">Metrics visualization coming soon</p>
                </div>
            </div>
        `;
    },

    async loadLogs() {
        document.getElementById('logs-content').innerHTML = `
            <div class="card">
                <div class="card-header">Request Logs</div>
                <div class="card-body">
                    <p style="color: var(--text-secondary);">Request log viewer coming soon</p>
                </div>
            </div>
        `;
    },

    showCreatePlatform() {
        document.getElementById('modal-title').textContent = 'Create Platform';
        document.getElementById('modal-body').innerHTML = `
            <form onsubmit="app.handleCreatePlatform(event)">
                <div class="form-group">
                    <label class="form-label">Platform ID</label>
                    <input type="text" name="id" class="form-input" placeholder="my-platform" required>
                </div>
                <div class="form-group">
                    <label class="form-label">Platform Name</label>
                    <input type="text" name="name" class="form-input" placeholder="My Platform" required>
                </div>
                <div class="form-group">
                    <label class="form-label">Enable Sticky Sessions</label>
                    <select name="sticky_enabled" class="form-input">
                        <option value="true">Yes</option>
                        <option value="false">No</option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">Enable Scheduled Rotation</label>
                    <select name="scheduled_rotation_enabled" class="form-input">
                        <option value="false">No</option>
                        <option value="true">Yes</option>
                    </select>
                </div>
                <div class="form-group">
                    <label class="form-label">Rotation Interval (e.g., "1h", "30m")</label>
                    <input type="text" name="scheduled_rotation_interval" class="form-input" placeholder="1h">
                </div>
                <div style="display: flex; gap: 12px; justify-content: flex-end;">
                    <button type="button" class="btn btn-secondary" onclick="app.closeModal()">Cancel</button>
                    <button type="submit" class="btn btn-primary">Create</button>
                </div>
            </form>
        `;
        document.getElementById('modal').classList.add('active');
    },

    async handleCreatePlatform(event) {
        event.preventDefault();
        const form = event.target;
        const data = {
            id: form.id.value,
            name: form.name.value,
            sticky_enabled: form.sticky_enabled.value === 'true',
            scheduled_rotation_enabled: form.scheduled_rotation_enabled.value === 'true'
        };

        if (data.scheduled_rotation_enabled && form.scheduled_rotation_interval.value) {
            data.scheduled_rotation_interval = form.scheduled_rotation_interval.value;
        }

        try {
            await this.api('/v1/platforms', {
                method: 'POST',
                body: JSON.stringify(data)
            });
            this.closeModal();
            this.showToast('Platform created successfully', 'success');
            this.loadPlatforms();
        } catch (error) {
            this.showToast(`Failed to create platform: ${error.message}`, 'error');
        }
    },

    async deletePlatform(id) {
        if (!confirm(`Delete platform "${id}"?`)) return;

        try {
            await this.api(`/v1/platforms/${encodeURIComponent(id)}`, {
                method: 'DELETE'
            });
            this.showToast('Platform deleted', 'success');
            this.loadPlatforms();
        } catch (error) {
            this.showToast(`Failed to delete platform: ${error.message}`, 'error');
        }
    },

    async deleteLease(platformId, account) {
        if (!confirm(`Delete lease for "${account}"?`)) return;

        try {
            await this.api(`/v1/platforms/${encodeURIComponent(platformId)}/leases/${encodeURIComponent(account)}`, {
                method: 'DELETE'
            });
            this.showToast('Lease deleted', 'success');
            this.loadLeases();
        } catch (error) {
            this.showToast(`Failed to delete lease: ${error.message}`, 'error');
        }
    },

    async deleteEndpoint(id) {
        if (!confirm(`Delete endpoint "${id}"?`)) return;

        try {
            await this.api(`/v1/endpoints/${encodeURIComponent(id)}`, {
                method: 'DELETE'
            });
            this.showToast('Endpoint deleted', 'success');
            this.loadEndpoints();
        } catch (error) {
            this.showToast(`Failed to delete endpoint: ${error.message}`, 'error');
        }
    },

    closeModal() {
        document.getElementById('modal').classList.remove('active');
    },

    showToast(message, type = 'success') {
        const toast = document.getElementById('toast');
        toast.textContent = message;
        toast.className = `toast ${type} active`;
        setTimeout(() => {
            toast.classList.remove('active');
        }, 3000);
    },

    formatDuration(seconds) {
        if (seconds < 60) return `${seconds}s`;
        if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
        if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
        return `${Math.floor(seconds / 86400)}d`;
    },

    formatTimestamp(ns) {
        if (!ns) return 'N/A';
        const date = new Date(ns / 1000000);
        return date.toLocaleString();
    },

    escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    },

    startPolling() {
        setInterval(() => {
            if (this.currentPage === 'dashboard') {
                this.loadDashboard();
            }
        }, 10000);
    }
};

document.addEventListener('DOMContentLoaded', () => app.init());
