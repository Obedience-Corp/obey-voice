package speechserve

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TokenFileName is the shared secret's name inside the speech credentials
// directory, a sibling of serve's own token file.
const TokenFileName = "token"

// LoadOrMintToken reads dir/token, creating it 0600 on first run. The token is
// the only thing standing between a LAN and this computer's GPU, so an empty
// file is an error rather than a silently open door.
func LoadOrMintToken(dir string) (token string, created bool, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fmt.Errorf("create speech credentials dir: %w", err)
	}
	path := filepath.Join(dir, TokenFileName)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		token = strings.TrimSpace(string(data))
		if token == "" {
			return "", false, fmt.Errorf("token file %s is empty — delete it to regenerate", path)
		}
		return token, false, nil
	case os.IsNotExist(err):
		token, err = generateToken()
		if err != nil {
			return "", false, err
		}
		if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
			return "", false, fmt.Errorf("store speech token: %w", err)
		}
		return token, true, nil
	default:
		return "", false, fmt.Errorf("read speech token: %w", err)
	}
}

func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate speech token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
