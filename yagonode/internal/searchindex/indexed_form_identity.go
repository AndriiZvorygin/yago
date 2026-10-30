package searchindex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/blevesearch/bleve/v2"
	bleveindex "github.com/blevesearch/bleve_index_api"
)

const indexedFormGeneration = 1

func indexedFormIdentity(document bleveDocument) string {
	identity := sha256.Sum256(fmt.Appendf(nil, "%d%#v", indexedFormGeneration, document))

	return hex.EncodeToString(identity[:])
}

func shardHoldsIndexedForm(shard bleve.Index, id string, document bleveDocument) bool {
	indexed, err := shard.Document(id)
	if err != nil || indexed == nil {
		return false
	}
	held := false
	indexed.VisitFields(func(field bleveindex.Field) {
		if field.Name() == storedCandidateField {
			held = bytes.Equal(field.Value(), []byte(document.Candidate))
		}
	})

	return held
}
