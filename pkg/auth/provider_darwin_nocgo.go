//go:build darwin && !cgo

package auth

// Native macOS Keychain access requires CGO. Never substitute encrypted-file
// storage when the native provider cannot be linked.
func Save(string, Metadata, Secrets, []byte) error       { return ErrUnsupportedPlatform }
func LoadMetadata(string) (*Metadata, error)             { return nil, ErrUnsupportedPlatform }
func Unlock(string, []byte) (*Metadata, *Secrets, error) { return nil, nil, ErrUnsupportedPlatform }
func Delete(string) error                                { return ErrUnsupportedPlatform }
func Has(string) bool                                    { return false }
func MigrateLegacy(string, []byte) error                 { return ErrUnsupportedPlatform }

func SavePrivy(string, Metadata, Secrets, []byte) error { return ErrUnsupportedPlatform }
func LoadPrivyMetadata(string) (*Metadata, error)       { return nil, ErrUnsupportedPlatform }
func UnlockPrivy(string, []byte) (*Metadata, *Secrets, error) {
	return nil, nil, ErrUnsupportedPlatform
}
func DeletePrivy(string) error { return ErrUnsupportedPlatform }
