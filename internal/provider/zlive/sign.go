package zlive

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math/big"
	"time"

	"kari/internal/config"
)

// signedRequest is the envelope zlive.st's player sends to /resolve. The
// server treats anything else as an untrusted request and returns a dead
// fallback URL.
type signedRequest struct {
	A string `json:"a"`
	B string `json:"b"`
	C string `json:"c"`
	D string `json:"d"`
	E string `json:"e"`
}

// signRequest encrypts payload under a key derived from ticket and date,
// producing the same envelope the site's browser client produces. The random
// IV is generated with crypto/rand; signRequestWithIV is the deterministic
// form used by tests.
func signRequest(ticket string, payload []byte, date string) (signedRequest, error) {
	iv := make([]byte, zliveIVBytes)
	if _, err := rand.Read(iv); err != nil {
		return signedRequest{}, fmt.Errorf("zlive sign: generate IV: %w", err)
	}
	return signRequestWithIV(ticket, payload, date, iv), nil
}

// zliveIVBytes is the nonce size the site's client passes to its random
// generator. The counter block is 16 bytes with the last byte pinned to 1.
const zliveIVBytes = 12

// signRequestWithIV derives the signing key, encrypts payload in counter mode,
// and appends a truncated polynomial MAC over the associated data and
// ciphertext. IV must be zliveIVBytes long; callers in production use
// signRequest so the nonce is random.
func signRequestWithIV(ticket string, payload []byte, date string, iv []byte) signedRequest {
	dayKey := sha256.Sum256([]byte(config.ZliveSigningSecret + ":" + date))
	macKey := hmacSHA256(dayKey[:], []byte("k6"))
	streamKey := hmacSHA256(macKey, []byte(ticket+"|v6"))

	var counter [16]byte
	copy(counter[:], iv)
	counter[len(counter)-1] = 1
	initialCounter := counter

	ciphertext := make([]byte, len(payload))
	for offset := 0; offset < len(payload); offset += aes.BlockSize {
		incrementCounter(counter[:])
		stream := aesEncrypt(streamKey, counter[:])
		end := offset + aes.BlockSize
		if end > len(payload) {
			end = len(payload)
		}
		for i := offset; i < end; i++ {
			ciphertext[i] = payload[i] ^ stream[i-offset]
		}
	}

	associated := []byte(ticket + "|" + date)
	ghashKey := new(big.Int).SetBytes(aesEncrypt(streamKey, make([]byte, aes.BlockSize)))
	tagValue := polynomialMAC(ghashKey, associated, ciphertext)
	tagInt := new(big.Int).Xor(tagValue, new(big.Int).SetBytes(aesEncrypt(streamKey, initialCounter[:])))

	return signedRequest{
		A: base64.StdEncoding.EncodeToString(ciphertext),
		B: base64.StdEncoding.EncodeToString(iv),
		C: base64.StdEncoding.EncodeToString(bigIntBytes(tagInt)),
		D: date,
		E: ticket,
	}
}

// hmacSHA256 returns the HMAC-SHA256 of msg under key.
func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

// aesEncrypt encrypts one 16-byte block with the given key.
func aesEncrypt(key, block []byte) []byte {
	cipherBlock, err := aes.NewCipher(key)
	if err != nil {
		panic("zlive: invalid AES key")
	}
	out := make([]byte, aes.BlockSize)
	cipherBlock.Encrypt(out, block)
	return out
}

// incrementCounter increments a big-endian counter block. The site starts
// its counter at 1 and increments before encrypting each block.
func incrementCounter(counter []byte) {
	for i := len(counter) - 1; i >= 0; i-- {
		counter[i]++
		if counter[i] != 0 {
			return
		}
	}
}

// gfReduction is the AES-GCM reduction polynomial x^128+x^7+x^2+x+1.
func gfReduction() *big.Int {
	return new(big.Int).Lsh(big.NewInt(0xe1), 120)
}

// gfMultiply multiplies two field elements without mutating either operand.
func gfMultiply(left, right *big.Int) *big.Int {
	product := new(big.Int)
	operand := new(big.Int).Set(left)
	one := big.NewInt(1)
	reduction := gfReduction()
	for bit := 127; bit >= 0; bit-- {
		if new(big.Int).And(new(big.Int).Rsh(right, uint(bit)), one).Sign() != 0 {
			product.Xor(product, operand)
		}
		if new(big.Int).And(operand, one).Sign() != 0 {
			operand = new(big.Int).Xor(new(big.Int).Rsh(operand, 1), reduction)
		} else {
			operand = new(big.Int).Rsh(operand, 1)
		}
	}
	return product
}

// polynomialMAC folds associated data and ciphertext into a 16-byte MAC.
// Trailing partial blocks are zero-padded, matching the site's client.
func polynomialMAC(key *big.Int, associated, ciphertext []byte) *big.Int {
	h := new(big.Int)
	fold := func(data []byte) {
		for offset := 0; offset < len(data); offset += aes.BlockSize {
			var block [aes.BlockSize]byte
			copy(block[:], data[offset:])
			h = gfMultiply(key, new(big.Int).Xor(h, new(big.Int).SetBytes(block[:])))
		}
	}
	fold(associated)
	fold(ciphertext)

	var lengths [aes.BlockSize]byte
	binary.BigEndian.PutUint32(lengths[4:8], uint32(len(associated)*8))
	binary.BigEndian.PutUint32(lengths[12:16], uint32(len(ciphertext)*8))
	h = gfMultiply(key, new(big.Int).Xor(h, new(big.Int).SetBytes(lengths[:])))
	return h
}

// bigIntBytes right-aligns a field element into a 16-byte buffer.
func bigIntBytes(value *big.Int) []byte {
	out := make([]byte, aes.BlockSize)
	raw := value.Bytes()
	copy(out[len(out)-len(raw):], raw)
	return out
}

// signingDate formats a UTC date the way the site's player does.
func signingDate(now time.Time) string {
	return now.UTC().Format("2006-01-02")
}
