package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// maskMark separates a masked address's host from the last characters it keeps.
const maskMark = "/…"

// masked reports whether an address is the masked form the configuration returns, not one that can be posted to.
func masked(address string) bool { return strings.Contains(address, maskMark) }

// maskAddress is what the configuration shows of a sealed address: its host and its last four characters.
func maskAddress(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return maskMark
	}
	return "https://" + u.Host + maskMark + address[max(0, len(address)-4):]
}

func addressDigest(channel, address string) string {
	sum := sha256.Sum256([]byte(channel + "\x00" + address))
	return hex.EncodeToString(sum[:])
}

func addressLocation(id string) secret.Location {
	return secret.Location{Table: "observability.channel_addresses", RowID: id, Purpose: "address"}
}

// SealAlertAddresses keeps out of c every address that is a bot's credential (a WeCom key, a Feishu hook's token): a
// new address is sealed, and c holds its masked form and digest instead; a masked one stands for the address the same
// channel already has, and is refused for any other. Call it in the transaction that then saves c.
func SealAlertAddresses(ctx context.Context, tx pg.Tx, keys *secret.Keyring, c *AlertConfiguration) error {
	held, err := readAlertConfiguration(ctx, tx, false)
	if err != nil {
		return err
	}
	before := map[string]AlertChannel{}
	for _, ch := range held.Channels {
		before[ch.ID] = ch
	}
	for i := range c.Channels {
		if err := sealAddress(ctx, tx, keys, &c.Channels[i], before[c.Channels[i].ID]); err != nil {
			return err
		}
	}
	return nil
}

func sealAddress(ctx context.Context, tx pg.Tx, keys *secret.Keyring, ch *AlertChannel, held AlertChannel) error {
	k, address, ok := kindOf(*ch)
	if !ok || !k.addressSecret {
		return nil
	}
	if masked(address) {
		heldKind, heldAddress, _ := kindOf(held)
		if heldKind != k || heldAddress != address || held.AddressDigest == "" {
			return ErrAlertConfiguration
		}
		ch.AddressDigest = held.AddressDigest
		return nil
	}
	if _, err := k.check(address); err != nil {
		return err
	}
	ch.AddressDigest = addressDigest(ch.ID, address)
	id := ch.ID + "/" + ch.AddressDigest
	s := keys.Seal(secret.New(address), addressLocation(id))
	// The same digest is the same address: a second save of it keeps the row.
	if _, err := tx.Exec(ctx, `INSERT INTO observability.channel_addresses(id,channel_id,digest,ciphertext,nonce,wrapped_dek,key_version)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO NOTHING`, id, ch.ID, ch.AddressDigest, s.Ciphertext, s.Nonce, s.WrappedDEK, int64(s.KeyVersion)); err != nil {
		return err
	}
	*k.field(ch) = maskAddress(address)
	return nil
}

// sendable reports whether a post may go to dest: a valid destination, and never a masked one whose sealed address
// could not be opened.
func sendable(k *channelKind, dest string) bool {
	_, err := k.check(dest)
	return err == nil && !masked(dest)
}

// openAddress puts the sealed address back into ch, a bot channel whose address is its credential, for its post.
func openAddress(ctx context.Context, tx pg.Tx, keys *secret.Keyring, ch *AlertChannel) error {
	k, _, ok := kindOf(*ch)
	if !ok || !k.addressSecret {
		return nil
	}
	id := ch.ID + "/" + ch.AddressDigest
	var s secret.Sealed
	err := tx.QueryRow(ctx, `SELECT ciphertext,nonce,wrapped_dek,key_version FROM observability.channel_addresses WHERE id=$1`, id).
		Scan(&s.Ciphertext, &s.Nonce, &s.WrappedDEK, &s.KeyVersion)
	if errors.Is(err, pg.ErrNoRows) {
		return nil // the channel's address went with it: the post fails on the masked form
	}
	if err != nil {
		return err
	}
	address, err := keys.Open(s, addressLocation(id))
	if err != nil {
		return err
	}
	if addressDigest(ch.ID, address.Reveal()) != ch.AddressDigest {
		return errors.New("bot: sealed address binding changed")
	}
	*k.field(ch) = address.Reveal()
	return nil
}
