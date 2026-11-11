package core

import (
	"crypto/sha256"
	"fmt"
)

type Principal struct {
	Namespace string
	CanWrite  bool
}

type TokenIndex struct {
	index map[[sha256.Size]byte]Principal
}

// NewTokenIndex 在启动时把明文 token 折叠成摘要，运行期只保留摘要。
func NewTokenIndex(namespaces []Namespace) (*TokenIndex, error) {
	index := make(map[[sha256.Size]byte]Principal)
	for _, namespace := range namespaces {
		for _, entry := range namespace.Tokens {
			body, perm, err := SplitToken(entry)
			if err != nil {
				return nil, err
			}
			digest := sha256.Sum256([]byte(body))
			if previous, ok := index[digest]; ok && previous.Namespace != namespace.Name {
				return nil, fmt.Errorf("token assigned to multiple namespaces")
			}
			index[digest] = Principal{Namespace: namespace.Name, CanWrite: perm == "rw"}
		}
	}
	return &TokenIndex{index: index}, nil
}

func (t *TokenIndex) Lookup(token string) (Principal, bool) {
	principal, ok := t.index[sha256.Sum256([]byte(token))]
	return principal, ok
}
