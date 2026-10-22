package cli

import "strings"

const (
	zeroAddress    = "0x0000000000000000000000000000000000000000"
	eip7528Address = "0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE"
)

// isNativeToken reports whether a user-supplied token denotes the chain's
// native asset: the literal "native", the zero address (what the sweep CSV
// exports) or the EIP-7528 0xEeee address, case-insensitively.
func isNativeToken(token string) bool {
	t := strings.TrimSpace(token)
	return strings.EqualFold(t, "native") || strings.EqualFold(t, zeroAddress) || strings.EqualFold(t, eip7528Address)
}

// normalizeToken maps every native alias to the CLI's internal "native"
// sentinel and leaves other inputs unchanged. Native still goes on the wire
// as the zero address (quoteTokenAddress).
func normalizeToken(token string) string {
	if isNativeToken(token) {
		return "native"
	}
	return strings.TrimSpace(token)
}
