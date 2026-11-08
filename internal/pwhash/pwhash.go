// Package pwhash makes and checks password hashes in the crypt(3) format of
// /etc/shadow: SHA-512 ($6$, made by default) and SHA-256 ($5$), as specified by
// Ulrich Drepper ("Unix crypt using SHA-256 and SHA-512").
package pwhash

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
)

const (
	alphabet      = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	defaultRounds = 5000
	minRounds     = 1000
	maxRounds     = 999999999
	maxSalt       = 16
)

// Hash makes a SHA-512 hash of password with a random salt.
func Hash(password string) (string, error) {
	b := make([]byte, maxSalt)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return Crypt(password, "$6$"+string(b))
}

// Verify reports whether password matches hash.
func Verify(password, hash string) bool {
	got, err := Crypt(password, hash)
	return err == nil && subtle.ConstantTimeCompare([]byte(got), []byte(hash)) == 1
}

// Valid checks that hash looks like a hash this package can verify.
func Valid(hash string) error {
	_, _, _, _, rest, err := parse(hash)
	if err != nil {
		return err
	}
	want := map[string]int{"$5$": 43, "$6$": 86}[hash[:3]]
	if len(rest) != want || strings.Trim(rest, alphabet) != "" {
		return fmt.Errorf("hash of %s: %d characters of [./0-9A-Za-z] expected", hash[:3], want)
	}
	return nil
}

// parse splits "$6$[rounds=N$]salt[$hash]".
func parse(setting string) (magic string, rounds int, custom bool, salt, rest string, err error) {
	if !strings.HasPrefix(setting, "$5$") && !strings.HasPrefix(setting, "$6$") {
		return "", 0, false, "", "", errors.New("only SHA-512 ($6$) and SHA-256 ($5$) hashes are supported")
	}
	magic, s := setting[:3], setting[3:]
	rounds = defaultRounds
	if r, ok := strings.CutPrefix(s, "rounds="); ok {
		n, tail, found := strings.Cut(r, "$")
		v, perr := strconv.Atoi(n)
		if !found || perr != nil {
			return "", 0, false, "", "", errors.New("bad rounds= in hash")
		}
		rounds, custom, s = min(max(v, minRounds), maxRounds), true, tail
	}
	salt, rest, _ = strings.Cut(s, "$")
	if len(salt) > maxSalt {
		salt = salt[:maxSalt]
	}
	return magic, rounds, custom, salt, rest, nil
}

// Crypt computes the hash of key with the method, rounds and salt of setting (a hash
// or just "$6$salt").
func Crypt(key, setting string) (string, error) {
	magic, rounds, custom, salt, _, err := parse(setting)
	if err != nil {
		return "", err
	}
	newHash, perm := sha512.New, perm512
	if magic == "$5$" {
		newHash, perm = sha256.New, perm256
	}
	sum := append(shaCrypt(newHash, []byte(key), []byte(salt), rounds), 0) // [len] = 0: the short last group
	var b strings.Builder
	b.WriteString(magic)
	if custom {
		fmt.Fprintf(&b, "rounds=%d$", rounds)
	}
	b.WriteString(salt)
	b.WriteByte('$')
	for _, t := range perm {
		w := uint(sum[t[0]])<<16 | uint(sum[t[1]])<<8 | uint(sum[t[2]])
		for i := 0; i < t[3]; i++ {
			b.WriteByte(alphabet[w&0x3f])
			w >>= 6
		}
	}
	return b.String(), nil
}

func shaCrypt(newHash func() hash.Hash, key, salt []byte, rounds int) []byte {
	h := newHash()
	h.Write(key)
	h.Write(salt)
	h.Write(key)
	altB := h.Sum(nil)
	size := len(altB)

	h = newHash()
	h.Write(key)
	h.Write(salt)
	i := len(key)
	for ; i > size; i -= size {
		h.Write(altB)
	}
	h.Write(altB[:i])
	for i = len(key); i > 0; i >>= 1 {
		if i&1 != 0 {
			h.Write(altB)
		} else {
			h.Write(key)
		}
	}
	a := h.Sum(nil)

	h = newHash()
	for range key {
		h.Write(key)
	}
	p := repeat(h.Sum(nil), len(key))

	h = newHash()
	for i := 0; i < 16+int(a[0]); i++ {
		h.Write(salt)
	}
	s := repeat(h.Sum(nil), len(salt))

	c := a
	for r := 0; r < rounds; r++ {
		h = newHash()
		if r&1 != 0 {
			h.Write(p)
		} else {
			h.Write(c)
		}
		if r%3 != 0 {
			h.Write(s)
		}
		if r%7 != 0 {
			h.Write(p)
		}
		if r&1 != 0 {
			h.Write(c)
		} else {
			h.Write(p)
		}
		c = h.Sum(nil)
	}
	return c
}

func repeat(sum []byte, n int) []byte {
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, sum[:min(len(sum), n-len(out))]...)
	}
	return out
}

// byte order of the output: groups of three bytes (and how many characters they give)
var perm512 = [][4]int{
	{0, 21, 42, 4}, {22, 43, 1, 4}, {44, 2, 23, 4}, {3, 24, 45, 4}, {25, 46, 4, 4}, {47, 5, 26, 4},
	{6, 27, 48, 4}, {28, 49, 7, 4}, {50, 8, 29, 4}, {9, 30, 51, 4}, {31, 52, 10, 4}, {53, 11, 32, 4},
	{12, 33, 54, 4}, {34, 55, 13, 4}, {56, 14, 35, 4}, {15, 36, 57, 4}, {37, 58, 16, 4}, {59, 17, 38, 4},
	{18, 39, 60, 4}, {40, 61, 19, 4}, {62, 20, 41, 4}, {64, 64, 63, 2},
}

var perm256 = [][4]int{
	{0, 10, 20, 4}, {21, 1, 11, 4}, {12, 22, 2, 4}, {3, 13, 23, 4}, {24, 4, 14, 4},
	{15, 25, 5, 4}, {6, 16, 26, 4}, {27, 7, 17, 4}, {18, 28, 8, 4}, {9, 19, 29, 4}, {32, 31, 30, 3},
}
