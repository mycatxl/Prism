package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/service"
)

func TestWriteServiceErrorHidesInternalDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	writeServiceError(rec, &service.ServiceError{
		Code: "INTERNAL", Message: "query /srv/prism/state.db: no such column password_hash",
		Err: errors.New("database failure"),
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	for _, leaked := range []string{"/srv/prism", "password_hash", "database failure"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Fatalf("internal response leaked %q: %s", leaked, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), `"code":"INTERNAL"`) {
		t.Fatalf("response = %s", rec.Body.String())
	}
}

func TestParseSorting_Defaults(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	s, err := ParseSorting(r, []string{"name", "id"}, "name", "asc")
	if err != nil {
		t.Fatal(err)
	}
	if s.SortBy != "name" {
		t.Errorf("SortBy = %q, want name", s.SortBy)
	}
	if s.SortOrder != "asc" {
		t.Errorf("SortOrder = %q, want asc", s.SortOrder)
	}
}

func TestParseSorting_Custom(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/test?sort_by=id&sort_order=desc", nil)
	s, err := ParseSorting(r, []string{"name", "id"}, "name", "asc")
	if err != nil {
		t.Fatal(err)
	}
	if s.SortBy != "id" {
		t.Errorf("SortBy = %q, want id", s.SortBy)
	}
	if s.SortOrder != "desc" {
		t.Errorf("SortOrder = %q, want desc", s.SortOrder)
	}
}

func TestParseSorting_InvalidField(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/test?sort_by=invalid", nil)
	_, err := ParseSorting(r, []string{"name", "id"}, "name", "asc")
	if err == nil {
		t.Error("expected error for invalid sort_by")
	}
}

func TestParseSorting_InvalidOrder(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/test?sort_order=sideways", nil)
	_, err := ParseSorting(r, []string{"name"}, "name", "asc")
	if err == nil {
		t.Error("expected error for invalid sort_order")
	}
}

func TestSortSlice_Asc(t *testing.T) {
	items := []string{"banana", "apple", "cherry"}
	SortSlice(items, Sorting{SortBy: "name", SortOrder: "asc"}, func(s string) string { return s })
	if items[0] != "apple" || items[1] != "banana" || items[2] != "cherry" {
		t.Errorf("unexpected order: %v", items)
	}
}

func TestSortSlice_Desc(t *testing.T) {
	items := []string{"banana", "apple", "cherry"}
	SortSlice(items, Sorting{SortBy: "name", SortOrder: "desc"}, func(s string) string { return s })
	if items[0] != "cherry" || items[1] != "banana" || items[2] != "apple" {
		t.Errorf("unexpected order: %v", items)
	}
}

func TestSortSlice_Empty(t *testing.T) {
	var items []string
	SortSlice(items, Sorting{SortBy: "name", SortOrder: "asc"}, func(s string) string { return s })
	// Should not panic.
}

func TestSortSlice_NoSortBy(t *testing.T) {
	items := []string{"banana", "apple"}
	SortSlice(items, Sorting{SortBy: "", SortOrder: "asc"}, func(s string) string { return s })
	// Should not sort, order unchanged.
	if items[0] != "banana" {
		t.Errorf("expected no sort, got %v", items)
	}
}

func TestPaginateSlice_OffsetOutOfRangeReturnsEmptySlice(t *testing.T) {
	page := PaginateSlice([]string{}, Pagination{Limit: 50, Offset: 0})
	if page == nil {
		t.Fatal("expected empty slice, got nil")
	}
	if len(page) != 0 {
		t.Fatalf("expected empty slice, got len=%d", len(page))
	}
}
