package journal

import (
	"database/sql"
	"errors"
	"time"
)

// RegistrationChallenge is short-lived consent material. The token is only
// for CompleteRegistration and must not be included in command output.
type RegistrationChallenge struct {
	Message, Token string
	ExpiresAt      time.Time
}

func (j *Journal) SaveRegistrationChallenge(agent string, c RegistrationChallenge) error {
	_, err := j.db.Exec(`INSERT INTO competition_registration(profile,network,account,origin,agent,message,challenge_token,expires_at)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(profile,network,account,origin,agent)
		DO UPDATE SET message=excluded.message,challenge_token=excluded.challenge_token,expires_at=excluded.expires_at`,
		j.binding.Profile, j.binding.Network, j.binding.Account, j.binding.Origin, agent, c.Message, c.Token, c.ExpiresAt.Format(time.RFC3339Nano))
	return err
}

func (j *Journal) GetRegistrationChallenge(agent string) (RegistrationChallenge, error) {
	var c RegistrationChallenge
	var expiry string
	err := j.db.QueryRow(`SELECT message,challenge_token,expires_at FROM competition_registration
		WHERE profile=? AND network=? AND account=? AND origin=? AND agent=?`,
		j.binding.Profile, j.binding.Network, j.binding.Account, j.binding.Origin, agent).Scan(&c.Message, &c.Token, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiry)
	if err != nil || c.Message == "" || c.Token == "" {
		return c, errors.New("invalid stored registration challenge")
	}
	return c, nil
}

// ConsumeRegistrationChallenge atomically reserves one attempt. Concurrent
// accepts cannot sign twice, and a replacement preview is not consumed.
func (j *Journal) ConsumeRegistrationChallenge(agent string, c RegistrationChallenge) error {
	r, err := j.db.Exec(`DELETE FROM competition_registration WHERE profile=? AND network=? AND account=? AND origin=? AND agent=?
		AND message=? AND challenge_token=? AND expires_at=?`,
		j.binding.Profile, j.binding.Network, j.binding.Account, j.binding.Origin, agent, c.Message, c.Token, c.ExpiresAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
