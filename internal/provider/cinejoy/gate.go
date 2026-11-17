package cinejoy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Cinejoy's resolution gateway does not accept plain JSON. Every call is an
// HPKE-style envelope: the client generates a P-256 key pair, derives two
// AES-256-GCM keys from ECDH with the gateway's static key, and sends the
// sealed payload with its ephemeral public key. The gateway answers with the
// same construction in the opposite direction.
const (
	// gateKeyID identifies the gateway's static P-256 key in the sealed body.
	gateKeyID = 2
	// gateEnvelopeVersion is the body format version the gateway accepts.
	gateEnvelopeVersion = 2
	// gateLabel prefixes both the HKDF info strings and the AES-GCM
	// additional data, binding a ciphertext to this gateway version.
	gateLabel = "lumen-gate-v2"
	// gateRequestInfo and gateResponseInfo are the per-direction HKDF info
	// strings. Deriving separate keys keeps a captured request ciphertext
	// from ever validating as a response, and vice versa.
	gateRequestInfo  = gateLabel + "|c2s"
	gateResponseInfo = gateLabel + "|s2c"
	// gateAADRequestType and gateAADResponseType are the direction bytes the
	// additional data carries after the label and before the key id.
	gateAADRequestType  = 0x01
	gateAADResponseType = 0x02
	// gateNonceSize is the AES-GCM nonce length used in both directions.
	gateNonceSize = 12
	// gatePublicKeyHex is the gateway's static uncompressed P-256 public key,
	// embedded in the site's sealing WASM (lumen-gate-v2, key id 2).
	gatePublicKeyHex = "045c88a0ae33c683a4872590b04b22f4774a4fe1c6ecf74785c74a919a71207ca94f9e6b5a7854c3aa3b44ee46bc444fea694b9f23bcd80f864ecbef48c1ef6e14"
	// gateHeaderSize covers key id, version, the ephemeral public key, and
	// the request nonce that precede the sealed payload.
	gateHeaderSize = 2 + 65 + gateNonceSize
)

// gateSession carries what is needed to open the gateway's reply to one
// sealed request: the response key and the additional data it was sealed with.
type gateSession struct {
	key [32]byte
	aad []byte
}

// parseGateKey decodes the gateway's static public key. It fails fast so a
// corrupt constant is never discovered at playback time.
func parseGateKey() (*ecdh.PublicKey, error) {
	raw, err := hex.DecodeString(gatePublicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode cinejoy gate key: %w", err)
	}
	key, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse cinejoy gate key: %w", err)
	}
	return key, nil
}

// sealRequest encrypts one gateway payload and returns the request body plus
// the session needed to open the response.
func sealRequest(gate *ecdh.PublicKey, payload []byte) ([]byte, gateSession, error) {
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, gateSession{}, fmt.Errorf("cinejoy ephemeral key: %w", err)
	}
	publicKey := ephemeral.PublicKey().Bytes()
	shared, err := ephemeral.ECDH(gate)
	if err != nil {
		return nil, gateSession{}, fmt.Errorf("cinejoy key exchange: %w", err)
	}

	requestKey := hkdfSHA256(shared, publicKey, []byte(gateRequestInfo))
	responseKey := hkdfSHA256(shared, publicKey, []byte(gateResponseInfo))

	nonce := make([]byte, gateNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, gateSession{}, fmt.Errorf("cinejoy nonce: %w", err)
	}

	sealed, err := sealAESGCM(requestKey, nonce, payload, gateAAD(publicKey, gateAADRequestType))
	if err != nil {
		return nil, gateSession{}, err
	}

	body := make([]byte, 0, gateHeaderSize+len(sealed))
	body = append(body, gateKeyID, gateEnvelopeVersion)
	body = append(body, publicKey...)
	body = append(body, nonce...)
	body = append(body, sealed...)

	return body, gateSession{
		key: responseKey,
		aad: gateAAD(publicKey, gateAADResponseType),
	}, nil
}

// openResponse decrypts and authenticates one gateway reply.
func (s gateSession) openResponse(raw []byte) ([]byte, error) {
	gcm, err := newAESGCM(s.key)
	if err != nil {
		return nil, err
	}
	if len(raw) < gateNonceSize+gcm.Overhead() {
		return nil, fmt.Errorf("cinejoy response is malformed (%d bytes)", len(raw))
	}
	plaintext, err := gcm.Open(nil, raw[:gateNonceSize], raw[gateNonceSize:], s.aad)
	if err != nil {
		return nil, fmt.Errorf("cinejoy response authentication failed: %w", err)
	}
	return plaintext, nil
}

// gateAAD builds the additional data that binds a ciphertext to one
// direction and one ephemeral public key.
func gateAAD(publicKey []byte, direction byte) []byte {
	aad := make([]byte, 0, len(gateLabel)+3+len(publicKey))
	aad = append(aad, gateLabel...)
	aad = append(aad, 0x00, direction, gateKeyID)
	return append(aad, publicKey...)
}

func sealAESGCM(key [32]byte, nonce, plaintext, aad []byte) ([]byte, error) {
	gcm, err := newAESGCM(key)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nil
}

func newAESGCM(key [32]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("cinejoy aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cinejoy aes gcm: %w", err)
	}
	return gcm, nil
}

// hkdfSHA256 derives one 32-byte key (RFC 5869 extract+expand). Only a single
// output block is needed because the requested length equals the hash size.
func hkdfSHA256(secret, salt, info []byte) [32]byte {
	if len(salt) == 0 {
		salt = make([]byte, sha256.Size)
	}
	extract := hmac.New(sha256.New, salt)
	extract.Write(secret)
	pseudorandomKey := extract.Sum(nil)

	expand := hmac.New(sha256.New, pseudorandomKey)
	expand.Write(info)
	expand.Write([]byte{0x01})

	var key [32]byte
	copy(key[:], expand.Sum(nil))
	return key
}
