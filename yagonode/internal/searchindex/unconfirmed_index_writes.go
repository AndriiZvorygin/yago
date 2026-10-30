package searchindex

import "github.com/blevesearch/bleve/v2"

type unconfirmedIndexWrites map[int]struct{}

func (writes unconfirmedIndexWrites) contains(shard int) bool {
	_, pending := writes[shard]

	return pending
}

func (writes *unconfirmedIndexWrites) record(shard int, err error) {
	if err == nil {
		delete(*writes, shard)
		return
	}
	if *writes == nil {
		*writes = make(unconfirmedIndexWrites)
	}
	(*writes)[shard] = struct{}{}
}

func (writes *unconfirmedIndexWrites) recordBatches(batches []*bleve.Batch, err error) {
	for shard, batch := range batches {
		if batch != nil {
			writes.record(shard, err)
		}
	}
}
