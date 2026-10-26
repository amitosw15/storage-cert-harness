# Partner host PATH and yq resolution. Safe to source from scripts or an interactive shell:
#   source ./ci/scripts/partner-host-env.sh
#
# Puts ~/.local/bin first (install-partner-tools drops yq/jq there) and clears bash's
# hashed path when /usr/bin/yq was removed but the shell still remembers it.
: "${PARTNER_USER_BIN:=${HOME}/.local/bin}"

case ":${PATH}:" in
*":${PARTNER_USER_BIN}:"*) ;;
*) export PATH="${PARTNER_USER_BIN}:${PATH}" ;;
esac

if [[ -z "${YQ:-}" && -x "${PARTNER_USER_BIN}/yq" ]]; then
	export YQ="${PARTNER_USER_BIN}/yq"
fi

if [[ -n "${BASH_VERSION:-}" ]]; then
	hash -d yq 2>/dev/null || true
	hash -d jq 2>/dev/null || true
fi

# Resolve mikefarah yq for partner scripts (prefer YQ, then ~/.local/bin/yq, then PATH).
partner_resolve_yq() {
	if [[ -n "${YQ:-}" && -x "${YQ}" ]]; then
		echo "${YQ}"
		return 0
	fi
	if [[ -x "${PARTNER_USER_BIN}/yq" ]]; then
		echo "${PARTNER_USER_BIN}/yq"
		return 0
	fi
	local found
	found="$(command -v yq 2>/dev/null || true)"
	if [[ -n "${found}" && -x "${found}" ]]; then
		echo "${found}"
		return 0
	fi
	return 1
}
