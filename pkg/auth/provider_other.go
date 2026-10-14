//go:build !darwin

package auth

// Linux and other non-Darwin targets retain the scrypt/XChaCha20-Poly1305 file provider.
func Save(profile string, md Metadata, s Secrets, passphrase []byte) error {
	return fileSave(deviceKey(profile), md, s, passphrase)
}
func LoadMetadata(profile string) (*Metadata, error) { return fileLoadMetadata(deviceKey(profile)) }
func Unlock(profile string, passphrase []byte) (*Metadata, *Secrets, error) {
	return fileUnlock(deviceKey(profile), passphrase)
}
func Delete(profile string) error        { return fileDelete(deviceKey(profile)) }
func Has(profile string) bool            { return fileHas(deviceKey(profile)) }
func MigrateLegacy(string, []byte) error { return ErrUnsupportedPlatform }

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
