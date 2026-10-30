package documentstore

import (
	"context"
	"fmt"

	"github.com/D4rk4/yago/yagonode/internal/vault"
)

func normalizedSubmissions(docs []Document) ([]Document, []string) {
	submissions := make([]Document, len(docs))
	normalizedURLs := make([]string, len(docs))
	for index, document := range docs {
		submissions[index] = retainSubmittedInlinks(normalizedDocument(document))
		normalizedURLs[index] = submissions[index].NormalizedURL
	}

	return submissions, normalizedURLs
}

func (d documentVault) acceptedCanonicalDocuments(
	ctx context.Context,
	submissions []Document,
	operation string,
) ([]Document, error) {
	canonical := make([]Document, 0, len(submissions))
	err := d.vault.View(ctx, func(tx *vault.Txn) error {
		for _, document := range submissions {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("context: %w", err)
			}
			revision, err := d.canonicalDocument(tx, document, nil)
			if err != nil {
				return err
			}
			if revision.kind != documentRevisionRejected {
				canonical = append(canonical, revision.document)
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}

	return canonical, nil
}
