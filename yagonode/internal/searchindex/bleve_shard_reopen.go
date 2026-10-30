package searchindex

import (
	"fmt"

	"github.com/blevesearch/bleve/v2"
)

func openBleveShardWithCurrentPolicy(path string) (bleve.Index, error) {
	index, err := bleve.OpenUsing(path, bleveDiskScorchConfiguration())
	if err != nil {
		return nil, fmt.Errorf("open scorch shard: %w", err)
	}

	return index, nil
}
