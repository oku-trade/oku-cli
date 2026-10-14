//go:build !darwin

package auth

// SavePrivy stores the Privy (Accounts session) credential for a profile.
func SavePrivy(profile string, md Metadata, s Secrets, passphrase []byte) error {
	md.Kind = KindPrivy
	return fileSave(privyKey(profile), md, s, passphrase)
}
func LoadPrivyMetadata(profile string) (*Metadata, error) {
	return fileLoadMetadata(privyKey(profile))
}
func UnlockPrivy(profile string, passphrase []byte) (*Metadata, *Secrets, error) {
	return fileUnlock(privyKey(profile), passphrase)
}
func DeletePrivy(profile string) error { return fileDelete(privyKey(profile)) }
