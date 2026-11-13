# Sourced by disposable binary checks. Deployment settings must never select
# their listeners, state, upstreams or payment credentials.
for prism_test_variable in ${!PRISM_@} ${!RESIN_@}; do
    case "$prism_test_variable" in
        PRISM_BIN|PRISM_SMOKE_KEEP|PRISM_BILLING_KEEP) ;;
        *) unset "$prism_test_variable" ;;
    esac
done
unset prism_test_variable
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy
export NO_PROXY='*' no_proxy='*'
