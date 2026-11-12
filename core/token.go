package core

import (
	"crypto/sha256"
	"fmt"
)

// Digest 把明文 token 折叠成定长摘要，运行期只保留摘要。
func Digest(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

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
			digest := Digest(body)
			if previous, ok := index[digest]; ok {
				if previous.Namespace != namespace.Name {
					return nil, fmt.Errorf("token assigned to multiple namespaces")
				}
				return nil, fmt.Errorf("namespace %q declares the same token twice", namespace.Name)
			}
			index[digest] = Principal{Namespace: namespace.Name, CanWrite: perm == "rw"}
		}
	}
	return &TokenIndex{index: index}, nil
}

func (t *TokenIndex) Lookup(token string) (Principal, bool) {
	principal, ok := t.index[Digest(token)]
	return principal, ok
}
