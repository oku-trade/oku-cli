//go:build !darwin

package auth

// Linux and other non-Darwin targets retain the scrypt/XChaCha20-Poly1305 file provider.
func Save(profile string, md Metadata, s Secrets, passphrase []byte) error {
	return fileSave(profile, md, s, passphrase)
}
func LoadMetadata(profile string) (*Metadata, error) { return fileLoadMetadata(profile) }
func Unlock(profile string, passphrase []byte) (*Metadata, *Secrets, error) {
	return fileUnlock(profile, passphrase)
}
func Delete(profile string) error        { return fileDelete(profile) }
func Has(profile string) bool            { return fileHas(profile) }
func MigrateLegacy(string, []byte) error { return ErrUnsupportedPlatform }
