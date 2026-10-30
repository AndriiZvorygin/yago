package documentstore

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"
)

const unchangedRevisionURL = "https://example.org/unchanged-revision"

var (
	unchangedRevisionFirstFetch  = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	unchangedRevisionSecondFetch = unchangedRevisionFirstFetch.Add(24 * time.Hour)
	unchangedRevisionThirdFetch  = unchangedRevisionSecondFetch.Add(24 * time.Hour)
)

func unchangedRevisionDocument(fetchedAt time.Time) Document {
	return Document{
		NormalizedURL: unchangedRevisionURL,
		Title:         "original",
		ExtractedText: "stable body",
		ContentHash:   "stable-hash",
		FetchStatus:   "fetched",
		PublishedAt:   unchangedRevisionFirstFetch.Add(-30 * 24 * time.Hour),
		FetchedAt:     fetchedAt,
		IndexedAt:     fetchedAt.Add(time.Minute),
		Outlinks:      []string{"https://example.org/next"},
		Inlinks:       []AnchorText{{URL: "https://example.org/source", Text: "anchor text"}},
		Metadata:      map[string]string{"author": "editor"},
	}
}

func changedDurableFieldValue(t *testing.T, field reflect.Value) {
	t.Helper()
	if field.Type() == reflect.TypeFor[time.Time]() {
		field.Set(reflect.ValueOf(field.Interface().(time.Time).Add(time.Hour)))

		return
	}
	switch field.Kind() {
	case reflect.String:
		field.SetString(field.String() + "-changed")
	case reflect.Bool:
		field.SetBool(!field.Bool())
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		field.SetUint(field.Uint() + 1)
	case reflect.Float64:
		field.SetFloat(field.Float() + 0.125)
	case reflect.Slice:
		field.Set(reflect.Append(field, reflect.New(field.Type().Elem()).Elem()))
	case reflect.Map:
		changed := reflect.MakeMap(field.Type())
		changed.SetMapIndex(reflect.ValueOf("changed"), reflect.ValueOf("changed"))
		field.Set(changed)
	case reflect.Struct:
		changedDurableFieldValue(t, field.Field(0))
	default:
		t.Fatalf("durable field kind %s has no change rule", field.Kind())
	}
}

func TestSameDurableDocumentComparesEveryDurableField(t *testing.T) {
	base := retainSubmittedInlinks(unchangedRevisionDocument(unchangedRevisionFirstFetch))
	if !sameDurableDocument(base, base) {
		t.Fatal("identical document reported as changed")
	}
	refreshedOnly := map[string]bool{"FetchedAt": true, "IndexedAt": true}
	documentType := reflect.TypeFor[Document]()
	for index := range documentType.NumField() {
		field := documentType.Field(index)
		if !field.IsExported() {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			changed := base
			changedDurableFieldValue(t, reflect.ValueOf(&changed).Elem().Field(index))
			same := sameDurableDocument(base, changed)
			if same != refreshedOnly[field.Name] {
				t.Fatalf("same durable document = %t", same)
			}
		})
	}
}

func TestSameDurableDocumentIgnoresSubmittedInlinkBookkeeping(t *testing.T) {
	previous := unchangedRevisionDocument(unchangedRevisionFirstFetch)
	incoming := previous
	incoming.submittedInlinks = []AnchorText{{URL: "https://example.org/submitted"}}
	incoming.submittedInlinksKnown = true
	if !sameDurableDocument(previous, incoming) {
		t.Fatal("submitted inlink bookkeeping reported as a durable change")
	}
}

func TestReceiveSkipsUnchangedDocumentAndKeepsSubstantiveRevisionTimes(t *testing.T) {
	directory, receiver, engine := openScriptedDocuments(t)
	first := unchangedRevisionDocument(unchangedRevisionFirstFetch)
	if _, err := receiver.Receive(t.Context(), []Document{first}); err != nil {
		t.Fatalf("first receive: %v", err)
	}
	key := scriptedOrderedDocumentKey(t, engine, unchangedRevisionURL)
	stored := bytes.Clone(engine.buckets[orderedDocumentBucketName][key])
	engine.putErrors[orderedDocumentBucketName] = errors.New("unchanged document rewritten")

	recrawl := unchangedRevisionDocument(unchangedRevisionSecondFetch)
	recrawl.PublishedAt = recrawl.PublishedAt.In(time.FixedZone("east", 3*60*60))
	recrawl.Inlinks = append(recrawl.Inlinks, AnchorText{
		URL:  "https://example.org/source",
		Text: " anchor   text ",
	})
	canonical, err := receiver.(CanonicalDocumentDirectory).CanonicalDocuments(
		t.Context(),
		[]Document{recrawl},
	)
	if err != nil || len(canonical) != 1 ||
		!canonical[0].FetchedAt.Equal(unchangedRevisionFirstFetch) {
		t.Fatalf("canonical unchanged documents = %#v, %v", canonical, err)
	}
	receipt, err := receiver.Receive(t.Context(), []Document{recrawl})
	if err != nil {
		t.Fatalf("unchanged receive: %v", err)
	}
	if receipt.Updated != 1 || receipt.Stored != 0 || len(receipt.CommittedDocuments) != 1 {
		t.Fatalf("unchanged receipt = %#v", receipt)
	}
	committed := receipt.CommittedDocuments[0]
	if !committed.FetchedAt.Equal(first.FetchedAt) ||
		!committed.IndexedAt.Equal(first.IndexedAt) {
		t.Fatalf("committed times = %v, %v", committed.FetchedAt, committed.IndexedAt)
	}
	if committed.PublishedAt.Location() != time.UTC ||
		!slices.Equal(committed.Inlinks, first.Inlinks) {
		t.Fatalf("committed canonical form = %v, %#v", committed.PublishedAt, committed.Inlinks)
	}
	if !committed.submittedInlinksKnown ||
		!slices.Equal(committed.submittedInlinks, first.Inlinks) {
		t.Fatalf("committed submitted inlinks = %#v", committed.submittedInlinks)
	}
	if !bytes.Equal(engine.buckets[orderedDocumentBucketName][key], stored) {
		t.Fatal("unchanged document record bytes changed")
	}
	if _, err := receiver.Receive(t.Context(), receipt.CommittedDocuments); err != nil {
		t.Fatalf("replayed unchanged receive: %v", err)
	}
	read, found, err := directory.Document(t.Context(), unchangedRevisionURL)
	if err != nil || !found || !read.FetchedAt.Equal(first.FetchedAt) {
		t.Fatalf("stored unchanged document = %#v, %t, %v", read, found, err)
	}
}

func TestReceiveWritesChangedFieldsDespiteSameContentHash(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Document)
	}{
		{name: "title", change: func(doc *Document) { doc.Title = "revised" }},
		{name: "outlinks", change: func(doc *Document) { doc.Outlinks = nil }},
		{name: "metadata", change: func(doc *Document) { doc.Metadata["author"] = "reviewer" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory, receiver := openDocuments(t)
			first := unchangedRevisionDocument(unchangedRevisionFirstFetch)
			if _, err := receiver.Receive(t.Context(), []Document{first}); err != nil {
				t.Fatalf("first receive: %v", err)
			}
			revised := unchangedRevisionDocument(unchangedRevisionSecondFetch)
			test.change(&revised)
			receipt, err := receiver.Receive(t.Context(), []Document{revised})
			if err != nil || receipt.Updated != 1 {
				t.Fatalf("revised receipt = %#v, %v", receipt, err)
			}
			read, found, err := directory.Document(t.Context(), unchangedRevisionURL)
			if err != nil || !found {
				t.Fatalf("stored revised document = %t, %v", found, err)
			}
			if !read.FetchedAt.Equal(unchangedRevisionSecondFetch) ||
				!sameDurableDocument(read, receipt.CommittedDocuments[0]) ||
				sameDurableDocument(read, first) {
				t.Fatalf("stored revised document = %#v", read)
			}
		})
	}
}

func TestReceiveInsertsMissingDocumentWithItsOwnTimes(t *testing.T) {
	directory, receiver := openDocuments(t)
	incoming := unchangedRevisionDocument(unchangedRevisionSecondFetch)
	receipt, err := receiver.Receive(t.Context(), []Document{incoming})
	if err != nil || receipt.Stored != 1 || receipt.Updated != 0 {
		t.Fatalf("missing document receipt = %#v, %v", receipt, err)
	}
	read, found, err := directory.Document(t.Context(), unchangedRevisionURL)
	if err != nil || !found || read.Title != incoming.Title ||
		!read.FetchedAt.Equal(incoming.FetchedAt) ||
		!read.IndexedAt.Equal(incoming.IndexedAt) {
		t.Fatalf("inserted document = %#v, %t, %v", read, found, err)
	}
}

func TestReceiveRepairsCorruptRecordEvenWhenIncomingMatchesIt(t *testing.T) {
	directory, receiver, engine := openScriptedDocuments(t)
	document := unchangedRevisionDocument(unchangedRevisionFirstFetch)
	if _, err := receiver.Receive(t.Context(), []Document{document}); err != nil {
		t.Fatalf("first receive: %v", err)
	}
	key := scriptedOrderedDocumentKey(t, engine, unchangedRevisionURL)
	engine.buckets[orderedDocumentBucketName][key] = []byte("{")

	receipt, err := receiver.Receive(t.Context(), []Document{document})
	if err != nil || receipt.Stored != 1 {
		t.Fatalf("repair receipt = %#v, %v", receipt, err)
	}
	read, found, err := directory.Document(t.Context(), unchangedRevisionURL)
	if err != nil || !found || read.Title != document.Title {
		t.Fatalf("repaired document = %#v, %t, %v", read, found, err)
	}
}

func TestReceiveOrdersSameBatchRevisionsByLatestWrite(t *testing.T) {
	revised := func(fetchedAt time.Time) Document {
		document := unchangedRevisionDocument(fetchedAt)
		document.Title = "revised"

		return document
	}
	tests := []struct {
		name          string
		seed          []Document
		batch         []Document
		wantTitle     string
		wantFetchedAt time.Time
		wantStored    int
		wantUpdated   int
	}{
		{
			name: "unchanged duplicate keeps first revision",
			seed: []Document{unchangedRevisionDocument(unchangedRevisionFirstFetch)},
			batch: []Document{
				revised(unchangedRevisionSecondFetch),
				revised(unchangedRevisionThirdFetch),
			},
			wantTitle:     "revised",
			wantFetchedAt: unchangedRevisionSecondFetch,
			wantUpdated:   2,
		},
		{
			name: "reverted duplicate overwrites earlier revision",
			seed: []Document{unchangedRevisionDocument(unchangedRevisionFirstFetch)},
			batch: []Document{
				revised(unchangedRevisionSecondFetch),
				unchangedRevisionDocument(unchangedRevisionThirdFetch),
			},
			wantTitle:     "original",
			wantFetchedAt: unchangedRevisionThirdFetch,
			wantUpdated:   2,
		},
		{
			name: "missing duplicate stores once",
			batch: []Document{
				unchangedRevisionDocument(unchangedRevisionSecondFetch),
				unchangedRevisionDocument(unchangedRevisionThirdFetch),
			},
			wantTitle:     "original",
			wantFetchedAt: unchangedRevisionSecondFetch,
			wantStored:    1,
			wantUpdated:   1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory, receiver := openDocuments(t)
			if _, err := receiver.Receive(t.Context(), test.seed); err != nil {
				t.Fatalf("seed receive: %v", err)
			}
			receipt, err := receiver.Receive(t.Context(), test.batch)
			if err != nil || receipt.Stored != test.wantStored ||
				receipt.Updated != test.wantUpdated || len(receipt.CommittedDocuments) != 2 {
				t.Fatalf("batch receipt = %#v, %v", receipt, err)
			}
			latest := receipt.CommittedDocuments[1]
			if latest.Title != test.wantTitle || !latest.FetchedAt.Equal(test.wantFetchedAt) {
				t.Fatalf("latest committed document = %#v", latest)
			}
			read, found, err := directory.Document(t.Context(), unchangedRevisionURL)
			if err != nil || !found || !sameDurableDocument(read, latest) ||
				!read.FetchedAt.Equal(test.wantFetchedAt) {
				t.Fatalf("stored batch document = %#v, %t, %v", read, found, err)
			}
		})
	}
}
