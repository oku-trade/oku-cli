//go:build linux

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dbus "github.com/godbus/dbus/v5"
)

// secretService stores one item per profile in the freedesktop Secret
// Service default collection (GNOME Keyring, KWallet, KeePassXC). It talks
// D-Bus directly through godbus (pure Go, no CGO) on a private session bus
// connection that is closed after every operation.
//
// It never falls back to the file store: a missing bus, a missing provider,
// or a locked collection the user did not unlock is ErrKeychainUnavailable.
type secretService struct {
	// promptTimeout bounds how long an unlock prompt from the desktop keyring
	// may stay open before it is dismissed.
	promptTimeout time.Duration
}

const (
	ssName       = "org.freedesktop.secrets"
	ssPath       = dbus.ObjectPath("/org/freedesktop/secrets")
	ssService    = "org.freedesktop.Secret.Service"
	ssCollection = "org.freedesktop.Secret.Collection"
	ssItem       = "org.freedesktop.Secret.Item"
	ssPrompt     = "org.freedesktop.Secret.Prompt"
	ssSession    = "org.freedesktop.Secret.Session"
	noPrompt     = dbus.ObjectPath("/")
)

// ssSecret is the Secret Service (oayays) secret struct.
type ssSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

func ssUnavailable(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrKeychainUnavailable, fmt.Sprintf(format, a...))
}

type ssConn struct {
	conn    *dbus.Conn
	session dbus.ObjectPath
	timeout time.Duration
}

func (s secretService) open() (*ssConn, error) {
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return nil, ssUnavailable("no D-Bus session bus (%v); the keyring store needs a desktop login session", err)
	}
	if err = conn.Auth(nil); err == nil {
		err = conn.Hello()
	}
	if err != nil {
		conn.Close()
		return nil, ssUnavailable("D-Bus session bus: %v", err)
	}
	c := &ssConn{conn: conn, timeout: s.promptTimeout}
	if c.timeout <= 0 {
		c.timeout = 2 * time.Minute
	}
	var ignored dbus.Variant
	if err = c.call(conn.Object(ssName, ssPath), ssService+".OpenSession", "plain", dbus.MakeVariant("")).Store(&ignored, &c.session); err != nil {
		conn.Close()
		var de dbus.Error
		if errors.As(err, &de) && strings.HasSuffix(de.Name, "ServiceUnknown") {
			return nil, ssUnavailable("no Secret Service provider on the session bus (install or start GNOME Keyring, KWallet or KeePassXC)")
		}
		return nil, ssUnavailable("Secret Service: %v", err)
	}
	return c, nil
}

func (c *ssConn) close() {
	if c.session != "" {
		_ = c.conn.Object(ssName, c.session).Call(ssSession+".Close", 0).Err
	}
	_ = c.conn.Close()
}

func (c *ssConn) call(obj dbus.BusObject, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return obj.CallWithContext(ctx, method, 0, args...)
}

// prompt runs a Secret Service prompt (usually the keyring unlock dialog)
// and reports its result. A dismissed or timed out prompt is unavailable,
// never "missing".
func (c *ssConn) prompt(path dbus.ObjectPath) (dbus.Variant, error) {
	if path == noPrompt || path == "" {
		return dbus.Variant{}, nil
	}
	match := []dbus.MatchOption{dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(ssPrompt), dbus.WithMatchMember("Completed")}
	if err := c.conn.AddMatchSignal(match...); err != nil {
		return dbus.Variant{}, ssUnavailable("keyring prompt: %v", err)
	}
	defer func() { _ = c.conn.RemoveMatchSignal(match...) }()
	ch := make(chan *dbus.Signal, 4)
	c.conn.Signal(ch)
	defer c.conn.RemoveSignal(ch)
	obj := c.conn.Object(ssName, path)
	if err := c.call(obj, ssPrompt+".Prompt", "").Err; err != nil {
		return dbus.Variant{}, ssUnavailable("keyring prompt: %v", err)
	}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	for {
		select {
		case sig := <-ch:
			if sig == nil || sig.Path != path || sig.Name != ssPrompt+".Completed" || len(sig.Body) != 2 {
				continue
			}
			if dismissed, _ := sig.Body[0].(bool); dismissed {
				return dbus.Variant{}, ssUnavailable("the desktop keyring stayed locked (prompt dismissed)")
			}
			v, _ := sig.Body[1].(dbus.Variant)
			return v, nil
		case <-timer.C:
			_ = c.call(obj, ssPrompt+".Dismiss").Err
			return dbus.Variant{}, ssUnavailable("the desktop keyring stayed locked (no unlock within %s)", c.timeout)
		}
	}
}

func (c *ssConn) unlock(paths []dbus.ObjectPath) error {
	if len(paths) == 0 {
		return nil
	}
	var unlocked []dbus.ObjectPath
	var p dbus.ObjectPath
	if err := c.call(c.conn.Object(ssName, ssPath), ssService+".Unlock", paths).Store(&unlocked, &p); err != nil {
		return ssUnavailable("unlocking keyring: %v", err)
	}
	if _, err := c.prompt(p); err != nil {
		return err
	}
	return nil
}

// defaultCollection returns the unlocked default collection (usually the
// login keyring).
func (c *ssConn) defaultCollection() (dbus.ObjectPath, error) {
	var coll dbus.ObjectPath
	if err := c.call(c.conn.Object(ssName, ssPath), ssService+".ReadAlias", "default").Store(&coll); err != nil {
		return "", ssUnavailable("finding the default keyring: %v", err)
	}
	if coll == noPrompt || coll == "" {
		return "", ssUnavailable("the Secret Service has no default keyring collection; create one in your keyring manager")
	}
	if err := c.unlock([]dbus.ObjectPath{coll}); err != nil {
		return "", err
	}
	return coll, nil
}

func ssAttributes(k key) (map[string]string, error) {
	account, err := keychainAccount(k)
	if err != nil {
		return nil, err
	}
	return map[string]string{"service": keychainService, "account": account, "profile": k.profile, "kind": k.kind}, nil
}

// search returns all matching items, unlocking locked ones first.
func (c *ssConn) search(k key) ([]dbus.ObjectPath, error) {
	attrs, err := ssAttributes(k)
	if err != nil {
		return nil, err
	}
	// Search on the stable identifiers only; label-like attributes may differ.
	query := map[string]string{"service": attrs["service"], "account": attrs["account"]}
	var unlocked, locked []dbus.ObjectPath
	if err := c.call(c.conn.Object(ssName, ssPath), ssService+".SearchItems", query).Store(&unlocked, &locked); err != nil {
		return nil, ssUnavailable("searching keyring: %v", err)
	}
	if err := c.unlock(locked); err != nil {
		return nil, err
	}
	return append(unlocked, locked...), nil
}

func (s secretService) Get(k key) ([]byte, error) {
	if k.profile == "" {
		return nil, errors.New("empty keyring profile")
	}
	c, err := s.open()
	if err != nil {
		return nil, err
	}
	defer c.close()
	items, err := c.search(k)
	if err != nil {
		return nil, err
	}
	switch len(items) {
	case 0:
		return nil, ErrNoCredentials
	case 1:
	default:
		return nil, errors.New("ambiguous keyring credential")
	}
	var sec ssSecret
	if err := c.call(c.conn.Object(ssName, items[0]), ssItem+".GetSecret", c.session).Store(&sec); err != nil {
		return nil, ssUnavailable("reading keyring item: %v", err)
	}
	return sec.Value, nil
}

func (s secretService) Put(k key, value []byte) error {
	if k.profile == "" {
		return errors.New("empty keyring profile")
	}
	attrs, err := ssAttributes(k)
	if err != nil {
		return err
	}
	c, err := s.open()
	if err != nil {
		return err
	}
	defer c.close()
	coll, err := c.defaultCollection()
	if err != nil {
		return err
	}
	props := map[string]dbus.Variant{
		ssItem + ".Label":      dbus.MakeVariant("Oku Agent (" + k.profile + ")"),
		ssItem + ".Attributes": dbus.MakeVariant(attrs),
	}
	secret := ssSecret{Session: c.session, Parameters: []byte{}, Value: value, ContentType: "application/json"}
	var item, p dbus.ObjectPath
	// replace=true updates the existing item with identical attributes.
	if err := c.call(c.conn.Object(ssName, coll), ssCollection+".CreateItem", props, secret, true).Store(&item, &p); err != nil {
		return ssUnavailable("writing keyring item: %v", err)
	}
	_, err = c.prompt(p)
	return err
}

func (s secretService) Remove(k key) error {
	if k.profile == "" {
		return errors.New("empty keyring profile")
	}
	c, err := s.open()
	if err != nil {
		return err
	}
	defer c.close()
	items, err := c.search(k)
	if err != nil {
		return err
	}
	for _, it := range items {
		var p dbus.ObjectPath
		if err := c.call(c.conn.Object(ssName, it), ssItem+".Delete").Store(&p); err != nil {
			return ssUnavailable("deleting keyring item: %v", err)
		}
		if _, err := c.prompt(p); err != nil {
			return err
		}
	}
	return nil
}

// probe checks that the session bus, a provider and a default collection are
// present and that the collection can be unlocked.
func (s secretService) probe() error {
	c, err := s.open()
	if err != nil {
		return err
	}
	defer c.close()
	_, err = c.defaultCollection()
	return err
}
