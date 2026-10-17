package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagonode/internal/documentimport"
)

const importMaximumRecords = 64

type importCheckpoint struct {
	Version                int    `json:"version"`
	SourceID               string `json:"sourceId"`
	DestinationFingerprint string `json:"destinationFingerprint"`
	InputSize              int64  `json:"inputSize"`
	InputModTime           int64  `json:"inputModTime"`
	Offset                 int64  `json:"offset"`
	Records                int64  `json:"records"`
	Imported               int64  `json:"imported"`
	Present                int64  `json:"alreadyPresent"`
	Conflicts              int64  `json:"conflicts"`
}

type importOptions struct {
	Input    string
	Endpoint string
	APIKey   string
	State    string
	DryRun   bool
}

type importResponse struct {
	Results []documentimport.Result `json:"results"`
}

type pendingRecord struct {
	value documentimport.Record
	end   int64
}

type importSession struct {
	options   importOptions
	endpoint  *url.URL
	file      *os.File
	info      os.FileInfo
	reader    *bufio.Reader
	statePath string
	state     importCheckpoint
	offset    int64
	pending   *pendingRecord
}

func importCorpus(ctx context.Context, opts importOptions, progress io.Writer) (resultErr error) {
	session, err := openImportSession(opts)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = closeImportInput(session.file, "close interchange input", resultErr)
	}()
	if opts.DryRun {
		return validateImportFile(session.reader, session.offset, progress)
	}
	return session.importDocuments(ctx, progress)
}

func openImportSession(opts importOptions) (session *importSession, resultErr error) {
	return openImportSessionUsing(opts, os.Open)
}

func openImportSessionUsing(
	opts importOptions,
	openInput func(string) (*os.File, error),
) (session *importSession, resultErr error) {
	endpoint, err := validateImportOptions(opts)
	if err != nil {
		return nil, err
	}
	file, err := openInput(opts.Input)
	if err != nil {
		return nil, fmt.Errorf("open interchange file: %w", err)
	}
	keepOpen := false
	defer func() {
		if !keepOpen {
			resultErr = closeImportInput(file, "close failed interchange input", resultErr)
		}
	}()
	info, err := importInputInfo(file)
	if err != nil {
		return nil, err
	}
	statePath := opts.State
	if statePath == "" {
		statePath = importCheckpointPath(opts.Input)
	}
	state, found, err := loadImportCheckpoint(statePath)
	if err != nil {
		return nil, err
	}
	if err := validateImportCheckpoint(state, found, info, endpoint); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(file, yagocrawlcontract.MaximumIngestBatchBytes+1)
	session = &importSession{
		options: opts, endpoint: endpoint, file: file, info: info,
		reader: reader, statePath: statePath, state: state, offset: state.Offset,
	}
	if found {
		err = session.restoreCheckpointHeader()
	} else {
		err = session.readInitialHeader()
	}
	if err != nil {
		return nil, err
	}
	keepOpen = true
	return session, nil
}

type importInputStat interface {
	Stat() (os.FileInfo, error)
}

func importInputInfo(file importInputStat) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect interchange input: %w", err)
	}
	return info, nil
}

func closeImportInput(file *os.File, operation string, resultErr error) error {
	if err := file.Close(); err != nil {
		return errors.Join(resultErr, fmt.Errorf("%s: %w", operation, err))
	}
	return resultErr
}

func validateImportOptions(opts importOptions) (*url.URL, error) {
	if opts.Input == "" {
		return nil, errors.New("input path is required")
	}
	endpoint, err := url.Parse(opts.Endpoint)
	if err != nil || endpoint.Host == "" ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.User != nil || endpoint.Fragment != "" {
		return nil, errors.New(
			"destination endpoint must be an absolute HTTP(S) URL without embedded credentials",
		)
	}
	if !opts.DryRun && strings.TrimSpace(opts.APIKey) == "" {
		return nil, errors.New("YAGO_API_KEY is required for import")
	}
	return endpoint, nil
}

func validateImportCheckpoint(
	state importCheckpoint,
	found bool,
	info os.FileInfo,
	endpoint *url.URL,
) error {
	if found &&
		(state.InputSize != info.Size() || state.InputModTime != info.ModTime().UnixNano()) {
		return errors.New("interchange file changed since the last checkpoint")
	}
	if found && state.DestinationFingerprint != destinationFingerprint(endpoint) {
		return errors.New("import checkpoint belongs to a different destination endpoint")
	}
	if state.Offset < 0 || state.Offset > info.Size() {
		return errors.New("import checkpoint offset is outside the input file")
	}
	return nil
}

func (session *importSession) restoreCheckpointHeader() error {
	if _, err := session.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek interchange header: %w", err)
	}
	session.reader.Reset(session.file)
	line, err := readBoundedLine(session.reader)
	if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
		return fmt.Errorf("read interchange header: %w", err)
	}
	header, err := parseInterchangeHeader(line)
	if err != nil || header.SourceID != session.state.SourceID {
		return errors.New("interchange header does not match the import checkpoint")
	}
	if _, err := session.file.Seek(session.state.Offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek import checkpoint offset: %w", err)
	}
	session.reader.Reset(session.file)
	return nil
}

func (session *importSession) readInitialHeader() error {
	line, err := readBoundedLine(session.reader)
	if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
		return fmt.Errorf("read interchange header: %w", err)
	}
	header, err := parseInterchangeHeader(line)
	if err != nil {
		return errors.New("invalid versioned YaCy interchange header")
	}
	session.offset += int64(len(line))
	session.state = importCheckpoint{
		Version:                1,
		SourceID:               header.SourceID,
		DestinationFingerprint: destinationFingerprint(session.endpoint),
		InputSize:              session.info.Size(),
		InputModTime:           session.info.ModTime().UnixNano(),
		Offset:                 session.offset,
	}
	if !session.options.DryRun {
		if err := verifyImportFileUnchanged(session.file, session.info); err != nil {
			return err
		}
		if err := saveCheckpoint(session.statePath, session.state); err != nil {
			return err
		}
	}
	return nil
}

func parseInterchangeHeader(line []byte) (interchangeHeader, error) {
	var header interchangeHeader
	if err := json.Unmarshal(line, &header); err != nil {
		return interchangeHeader{}, fmt.Errorf("decode interchange header: %w", err)
	}
	if header.Type != "header" || header.Version != 1 || strings.TrimSpace(header.SourceID) == "" {
		return interchangeHeader{}, errors.New("invalid interchange header fields")
	}
	return header, nil
}

func (session *importSession) importDocuments(ctx context.Context, progress io.Writer) error {
	client := &http.Client{Timeout: 2 * time.Minute}
	for {
		batch, batchEnd, err := session.nextBatch()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return writeImportSummary(progress, session.state)
		}
		results, err := sendImportBatch(
			ctx, client, session.endpoint.String(), session.options.APIKey, batch,
		)
		if err != nil {
			return fmt.Errorf("import batch ending at byte %d: %w", batchEnd, err)
		}
		if err := validateImportResults(batch, results); err != nil {
			return fmt.Errorf("validate destination receipt: %w", err)
		}
		session.recordResults(results)
		session.state.Offset = batchEnd
		session.state.Records += int64(len(batch))
		if err := verifyImportFileUnchanged(session.file, session.info); err != nil {
			return err
		}
		if err := saveCheckpoint(session.statePath, session.state); err != nil {
			return err
		}
		if err := writeImportProgress(progress, session.state); err != nil {
			return err
		}
		if session.pending == nil && session.offset == session.info.Size() {
			return writeImportSummary(progress, session.state)
		}
	}
}

func (session *importSession) nextBatch() ([]documentimport.Record, int64, error) {
	return session.nextBatchUsing(session.nextRecord)
}

func (session *importSession) nextBatchUsing(
	readNext func() (*pendingRecord, bool, error),
) ([]documentimport.Record, int64, error) {
	batch := make([]documentimport.Record, 0, importMaximumRecords)
	batchEnd := session.state.Offset
	for len(batch) < importMaximumRecords {
		next, eof, err := readNext()
		if err != nil {
			return nil, 0, err
		}
		if eof {
			break
		}
		if next == nil {
			return nil, 0, errors.New("interchange reader returned no record")
		}
		batch = append(batch, next.value)
		encoded, _ := json.Marshal(documentimport.Request{Version: 1, Records: batch})
		if len(encoded) > yagocrawlcontract.MaximumIngestBatchBytes {
			batch = batch[:len(batch)-1]
			if len(batch) == 0 {
				return nil, 0, fmt.Errorf(
					"record %q exceeds the Yago import batch limit",
					next.value.ID,
				)
			}
			session.pending = next
			break
		}
		batchEnd = next.end
	}
	return batch, batchEnd, nil
}

func (session *importSession) nextRecord() (*pendingRecord, bool, error) {
	if session.pending != nil {
		next := session.pending
		session.pending = nil
		return next, false, nil
	}
	line, err := readBoundedLine(session.reader)
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return nil, true, nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("read interchange record: %w", err)
	}
	session.offset += int64(len(line))
	var envelope interchangeRecord
	if err := json.Unmarshal(line, &envelope); err != nil ||
		envelope.Type != "document" || envelope.Version != 1 {
		return nil, false, fmt.Errorf(
			"invalid interchange record after byte %d",
			session.state.Offset,
		)
	}
	if err := validateImportRecord(envelope.Record); err != nil {
		return nil, false, fmt.Errorf("invalid source record %q: %w", envelope.Record.ID, err)
	}
	return &pendingRecord{value: envelope.Record, end: session.offset}, false, nil
}

func (session *importSession) recordResults(results []documentimport.Result) {
	for _, result := range results {
		switch result.Status {
		case "imported":
			session.state.Imported++
		case "already-present":
			session.state.Present++
		case "conflict":
			session.state.Conflicts++
		}
	}
}

func writeImportProgress(progress io.Writer, state importCheckpoint) error {
	if _, err := fmt.Fprintf(
		progress,
		"processed %d records (imported %d, already present %d, conflicts %d)\n",
		state.Records,
		state.Imported,
		state.Present,
		state.Conflicts,
	); err != nil {
		return fmt.Errorf("write import progress: %w", err)
	}
	return nil
}

func destinationFingerprint(endpoint *url.URL) string {
	sum := sha256.Sum256([]byte(endpoint.String()))
	return hex.EncodeToString(sum[:])
}

func validateImportFile(reader *bufio.Reader, offset int64, progress io.Writer) error {
	var count int64
	for {
		line, err := readBoundedLine(reader)
		if errors.Is(err, io.EOF) && len(line) == 0 {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		var envelope interchangeRecord
		if json.Unmarshal(line, &envelope) != nil || envelope.Type != "document" ||
			envelope.Version != 1 {
			return fmt.Errorf("invalid interchange record after byte %d", offset)
		}
		if err := validateImportRecord(envelope.Record); err != nil {
			return fmt.Errorf("invalid source record %q: %w", envelope.Record.ID, err)
		}
		offset += int64(len(line))
		count++
	}
	_, err := fmt.Fprintf(
		progress,
		"dry-run: validated %d interchange records; no destination or checkpoint was changed\n",
		count,
	)
	if err != nil {
		return fmt.Errorf("write dry-run progress: %w", err)
	}
	return nil
}

func validateImportRecord(record documentimport.Record) error {
	if strings.TrimSpace(record.ID) == "" || len(record.ID) > 512 {
		return errors.New("missing or oversized record ID")
	}
	if len(record.Document.ExtractedText) > yagocrawlcontract.MaximumDocumentTextBytes {
		return errors.New("extracted text exceeds Yago's 1 MiB limit")
	}
	if len(record.Document.Title) > yagocrawlcontract.MaximumDocumentTitleBytes ||
		len(record.Document.Headings) > yagocrawlcontract.MaximumDocumentHeadings {
		return errors.New("document title or heading count exceeds Yago's limit")
	}
	for _, heading := range record.Document.Headings {
		if len(heading) > yagocrawlcontract.MaximumDocumentHeadingBytes {
			return errors.New("document heading exceeds Yago's size limit")
		}
	}
	if record.Document.NormalizedURL == "" {
		return errors.New("missing normalized URL")
	}
	if err := validateInterchangeURL(record.Document.NormalizedURL); err != nil {
		return errors.New("normalized URL must be a supported absolute HTTP(S) URL")
	}
	if record.Document.CanonicalURL != "" {
		if err := validateInterchangeURL(record.Document.CanonicalURL); err != nil {
			return errors.New("canonical URL must be a supported absolute HTTP(S) URL")
		}
	}
	if len(record.Document.Outlinks) > yagocrawlcontract.MaximumDocumentOutlinks ||
		len(
			record.Document.Inlinks,
		)+len(
			record.Document.OutboundAnchors,
		) > yagocrawlcontract.MaximumDocumentAnchors ||
		len(record.Document.Metadata) > yagocrawlcontract.MaximumDocumentMetadata {
		return errors.New("document exceeds Yago's link or metadata count limits")
	}
	metadataBytes := 0
	for key, value := range record.Document.Metadata {
		metadataBytes += len(key) + len(value)
	}
	if metadataBytes > yagocrawlcontract.MaximumDocumentMetadataBytes {
		return errors.New("document metadata exceeds Yago's size limit")
	}
	if record.NextVisitAt != nil && record.NextVisitAt.IsZero() {
		return errors.New("nextVisitAt must be a non-zero timestamp")
	}
	return nil
}

func validateInterchangeURL(raw string) error {
	if len(raw) > yagocrawlcontract.MaximumCrawlURLBytes {
		return errors.New("URL exceeds Yago's identity length limit")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.Fragment != "" {
		return errors.New("URL is not an absolute HTTP(S) URL without credentials or fragment")
	}
	return nil
}

func writeImportSummary(progress io.Writer, state importCheckpoint) error {
	_, err := fmt.Fprintf(
		progress,
		"import complete: %d records processed; %d imported, %d already present, %d conflicts preserved\n",
		state.Records,
		state.Imported,
		state.Present,
		state.Conflicts,
	)
	if err != nil {
		return fmt.Errorf("write import summary: %w", err)
	}
	return nil
}

func readBoundedLine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 4096)
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > yagocrawlcontract.MaximumIngestBatchBytes {
			return nil, errors.New("interchange line exceeds batch size limit")
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return line, fmt.Errorf("read interchange line: %w", err)
		}
		return line, nil
	}
}

func sendImportBatch(
	ctx context.Context,
	client *http.Client,
	endpoint, apiKey string,
	records []documentimport.Record,
) (results []documentimport.Result, resultErr error) {
	body, _ := json.Marshal(documentimport.Request{Version: 1, Records: records})
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("build import request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send import batch: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close import response: %w", err))
		}
	}()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"destination returned HTTP %d; batch checkpoint was not advanced",
			response.StatusCode,
		)
	}
	var decoded importResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode destination response: %w", err)
	}
	return decoded.Results, nil
}

func validateImportResults(
	requested []documentimport.Record,
	results []documentimport.Result,
) error {
	if len(requested) != len(results) {
		return errors.New("destination returned a different number of import results")
	}
	for index, result := range results {
		if result.ID != requested[index].ID {
			return errors.New("destination response record IDs do not match the request")
		}
		switch result.Status {
		case "imported", "already-present", "conflict":
		default:
			return fmt.Errorf("destination returned unsupported import status %q", result.Status)
		}
	}
	return nil
}

func loadImportCheckpoint(path string) (importCheckpoint, bool, error) {
	data, err := os.ReadFile(
		path,
	) // #nosec G304 -- checkpoint path is explicitly selected by the local operator.
	if errors.Is(err, os.ErrNotExist) {
		return importCheckpoint{}, false, nil
	}
	if err != nil {
		return importCheckpoint{}, false, fmt.Errorf("read import checkpoint: %w", err)
	}
	var state importCheckpoint
	if err := json.Unmarshal(
		data,
		&state,
	); err != nil || state.Version != 1 || state.Offset < 0 ||
		state.SourceID == "" {
		return importCheckpoint{}, false, errors.New("invalid import checkpoint")
	}
	return state, true, nil
}

func verifyImportFileUnchanged(file *os.File, original os.FileInfo) error {
	current, err := file.Stat()
	if err != nil {
		return fmt.Errorf("verify input file: %w", err)
	}
	if current.Size() != original.Size() ||
		current.ModTime().UnixNano() != original.ModTime().UnixNano() {
		return errors.New(
			"interchange file changed during import; batch checkpoint was not advanced",
		)
	}
	return nil
}

func importCheckpointPath(input string) string {
	return filepath.Clean(input) + ".import.state.json"
}
