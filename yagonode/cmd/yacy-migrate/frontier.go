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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagomodel"
)

const (
	maximumFrontierSeenURLs = 1_000_000
)

type frontierImportOptions struct {
	Input        string
	Profiles     string
	Endpoint     string
	APIKey       string
	State        string
	BatchRecords int
	MaxRecords   int
	DryRun       bool
}

type frontierFileHeader struct {
	Type     string `json:"type"`
	Version  int    `json:"version"`
	SourceID string `json:"sourceId"`
}

type yaCyFrontierRecord struct {
	Type           string `json:"type"`
	Version        int    `json:"version"`
	Stack          string `json:"stack"`
	URL            string `json:"url"`
	ProfileHandle  string `json:"profileHandle"`
	Depth          int    `json:"depth"`
	Anchor         string `json:"anchor"`
	InitiatorHex   string `json:"initiatorHex"`
	QueuedAtMillis int64  `json:"queuedAtMillis"`
}

type yaCyProfileRecord struct {
	Type    string            `json:"type"`
	Version int               `json:"version"`
	State   string            `json:"state"`
	Handle  string            `json:"handle"`
	Fields  map[string]string `json:"fields"`
}

type frontierImportCheckpoint struct {
	Version                int    `json:"version"`
	SourceID               string `json:"sourceId"`
	InputSHA256            string `json:"inputSha256"`
	ProfilesSHA256         string `json:"profilesSha256"`
	DestinationFingerprint string `json:"destinationFingerprint,omitempty"`
	InputSize              int64  `json:"inputSize"`
	InputModTime           int64  `json:"inputModTime"`
	ProfilesSize           int64  `json:"profilesSize"`
	ProfilesModTime        int64  `json:"profilesModTime"`
	Offset                 int64  `json:"offset"`
	Records                int64  `json:"records"`
	UniqueRequests         int64  `json:"uniqueRequests"`
	EnqueuedRequests       int64  `json:"enqueuedRequests"`
	AlreadyIndexed         int64  `json:"alreadyIndexed"`
	DuplicateRequests      int64  `json:"duplicateRequests"`
	BlockedCredentialURLs  int64  `json:"blockedCredentialURLs"`
	BlockedInvalidURLs     int64  `json:"blockedInvalidURLs"`
	Complete               bool   `json:"complete,omitempty"`
}

type frontierImportResponse struct {
	EnqueuedRequests int  `json:"enqueuedRequests"`
	AlreadyIndexed   int  `json:"alreadyIndexed"`
	DuplicateBatch   bool `json:"duplicateBatch,omitempty"`
}

type frontierSourceProfile struct {
	sourceHandle string
	profile      yagocrawlcontract.CrawlProfile
}

func importFrontier(
	ctx context.Context,
	opts frontierImportOptions,
	progress io.Writer,
) (resultErr error) {
	if err := validateFrontierImportOptions(opts); err != nil {
		return err
	}
	files, err := openFrontierImportFiles(opts)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = files.close(resultErr)
	}()
	setup, err := loadFrontierImportSetup(opts, files)
	if err != nil {
		return err
	}
	return runPreparedFrontierImport(ctx, opts, progress, files, setup)
}

type frontierImportFiles struct {
	input       *os.File
	inputInfo   os.FileInfo
	profiles    *os.File
	profileInfo os.FileInfo
}

func openFrontierImportFiles(opts frontierImportOptions) (*frontierImportFiles, error) {
	input, err := os.Open(opts.Input) // #nosec G304 -- selected by local migration operator.
	if err != nil {
		return nil, fmt.Errorf("open YaCy frontier: %w", err)
	}
	inputInfo, err := input.Stat()
	if err != nil {
		return nil, closeImportInput(
			input,
			"stat YaCy frontier",
			fmt.Errorf("stat YaCy frontier: %w", err),
		)
	}
	profiles, err := os.Open(
		opts.Profiles,
	) // #nosec G304 -- selected by local migration operator.
	if err != nil {
		return nil, closeImportInput(
			input,
			"close YaCy frontier",
			fmt.Errorf("open YaCy profile sidecar: %w", err),
		)
	}
	profileInfo, err := profiles.Stat()
	if err != nil {
		resultErr := fmt.Errorf("stat YaCy profile sidecar: %w", err)
		resultErr = closeImportInput(profiles, "close YaCy profile sidecar", resultErr)
		resultErr = closeImportInput(input, "close YaCy frontier", resultErr)
		return nil, resultErr
	}
	return &frontierImportFiles{
		input: input, inputInfo: inputInfo, profiles: profiles, profileInfo: profileInfo,
	}, nil
}

func (f *frontierImportFiles) close(resultErr error) error {
	resultErr = closeImportInput(f.profiles, "close YaCy profile sidecar", resultErr)
	return closeImportInput(f.input, "close YaCy frontier", resultErr)
}

type frontierImportSetup struct {
	profiles        map[string]frontierSourceProfile
	profileSource   string
	inputHash       string
	profilesHash    string
	endpoint        *url.URL
	statePath       string
	state           frontierImportCheckpoint
	checkpointFound bool
}

func loadFrontierImportSetup(
	opts frontierImportOptions,
	files *frontierImportFiles,
) (frontierImportSetup, error) {
	profilesHash, err := hashOpenFile(files.profiles)
	if err != nil {
		return frontierImportSetup{}, fmt.Errorf("fingerprint YaCy profile sidecar: %w", err)
	}
	profiles, profileSource, err := loadYaCyProfiles(files.profiles)
	if err != nil {
		return frontierImportSetup{}, err
	}
	inputHash, err := hashOpenFile(files.input)
	if err != nil {
		return frontierImportSetup{}, fmt.Errorf("fingerprint YaCy frontier: %w", err)
	}
	endpoint, err := validateFrontierEndpoint(opts.Endpoint, opts.DryRun)
	if err != nil {
		return frontierImportSetup{}, err
	}
	statePath := opts.State
	if statePath == "" {
		statePath = filepath.Clean(opts.Input) + ".frontier.import.state.json"
	}
	state, found, err := loadFrontierImportCheckpoint(statePath)
	if err != nil {
		return frontierImportSetup{}, err
	}
	validation := frontierCheckpointValidation{
		found: found, inputInfo: files.inputInfo, profileInfo: files.profileInfo,
		inputHash: inputHash, profilesHash: profilesHash, sourceID: profileSource,
		endpoint: endpoint, dryRun: opts.DryRun,
	}
	if err := validateFrontierCheckpoint(state, validation); err != nil {
		return frontierImportSetup{}, err
	}
	if opts.DryRun {
		found = false
		state = frontierImportCheckpoint{}
	}
	return frontierImportSetup{
		profiles: profiles, profileSource: profileSource, inputHash: inputHash,
		profilesHash: profilesHash, endpoint: endpoint, statePath: statePath,
		state: state, checkpointFound: found,
	}, nil
}

func runPreparedFrontierImport(
	ctx context.Context,
	opts frontierImportOptions,
	progress io.Writer,
	files *frontierImportFiles,
	setup frontierImportSetup,
) error {
	reader := bufio.NewReaderSize(files.input, 64<<10)
	header, headerEnd, err := readFrontierHeader(reader)
	if err != nil {
		return err
	}
	if header.SourceID != setup.profileSource {
		return errors.New("YaCy frontier and profile sidecar source IDs do not match")
	}
	if !setup.checkpointFound {
		setup.state = newFrontierImportCheckpoint(header, headerEnd, files, setup)
		if !opts.DryRun {
			setup.state.DestinationFingerprint = destinationFingerprint(setup.endpoint)
			if err := saveCheckpoint(setup.statePath, setup.state); err != nil {
				return err
			}
		}
	}
	seen, err := seekFrontierResumePoint(
		files.input,
		reader,
		headerEnd,
		setup.state,
		setup.profiles,
	)
	if err != nil {
		return err
	}
	runner := &frontierImportRunner{
		ctx: ctx, opts: opts, progress: progress, input: files.input,
		inputInfo: files.inputInfo, reader: reader, profiles: setup.profiles,
		seen: seen, endpoint: setup.endpoint, statePath: setup.statePath,
		state: setup.state, client: &http.Client{Timeout: 90 * time.Second},
		batchEnd: setup.state.Offset, offset: setup.state.Offset,
	}
	return runner.run()
}

func newFrontierImportCheckpoint(
	header frontierFileHeader,
	headerEnd int64,
	files *frontierImportFiles,
	setup frontierImportSetup,
) frontierImportCheckpoint {
	return frontierImportCheckpoint{
		Version: 1, SourceID: header.SourceID, InputSHA256: setup.inputHash,
		ProfilesSHA256: setup.profilesHash, InputSize: files.inputInfo.Size(),
		InputModTime: files.inputInfo.ModTime().UnixNano(), ProfilesSize: files.profileInfo.Size(),
		ProfilesModTime: files.profileInfo.ModTime().UnixNano(), Offset: headerEnd,
	}
}

func seekFrontierResumePoint(
	input *os.File,
	reader *bufio.Reader,
	headerEnd int64,
	state frontierImportCheckpoint,
	profiles map[string]frontierSourceProfile,
) (map[string]struct{}, error) {
	seen := make(map[string]struct{})
	if state.Offset > headerEnd {
		var err error
		seen, err = rebuildFrontierSeen(input, state.Offset, profiles)
		if err != nil {
			return nil, fmt.Errorf("restore frontier deduplication state: %w", err)
		}
	}
	seekTo := headerEnd
	if state.Offset > headerEnd {
		seekTo = state.Offset
	}
	if _, err := input.Seek(seekTo, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek YaCy frontier to resume point: %w", err)
	}
	reader.Reset(input)
	return seen, nil
}

func validateFrontierImportOptions(opts frontierImportOptions) error {
	if strings.TrimSpace(opts.Input) == "" || strings.TrimSpace(opts.Profiles) == "" {
		return errors.New("-input and -profiles are required")
	}
	if opts.BatchRecords < 1 ||
		opts.BatchRecords > yagocrawlcontract.MaximumCrawlOrderImportBatchRecords {
		return fmt.Errorf(
			"-batch-records must be between 1 and %d",
			yagocrawlcontract.MaximumCrawlOrderImportBatchRecords,
		)
	}
	if opts.MaxRecords < 0 {
		return errors.New("-max-records must not be negative")
	}
	if !opts.DryRun && opts.APIKey == "" {
		return errors.New("YAGO_API_KEY is required for a live frontier import")
	}
	return nil
}

func validateFrontierEndpoint(raw string, dryRun bool) (*url.URL, error) {
	if raw == "" && dryRun {
		return &url.URL{
			Scheme: "http",
			Host:   "dry-run.invalid",
			Path:   "/admin/crawl/frontier/import",
		}, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Path != "/admin/crawl/frontier/import" {
		return nil, errors.New(
			"-endpoint must be an HTTP(S) /admin/crawl/frontier/import URL without credentials or query",
		)
	}
	return parsed, nil
}

func readFrontierHeader(reader *bufio.Reader) (frontierFileHeader, int64, error) {
	line, err := readBoundedLine(reader)
	if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
		return frontierFileHeader{}, 0, fmt.Errorf("read YaCy frontier header: %w", err)
	}
	var header frontierFileHeader
	if json.Unmarshal(line, &header) != nil ||
		(header.Type != "yacy-crawl-frontier" && header.Type != "yacy-queue-frontier") ||
		header.Version != 1 || !validFrontierSourceID(header.SourceID) {
		return frontierFileHeader{}, 0, errors.New("invalid versioned YaCy frontier header")
	}
	return header, int64(len(line)), nil
}

func loadYaCyProfiles(file *os.File) (map[string]frontierSourceProfile, string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", fmt.Errorf("seek YaCy profile sidecar: %w", err)
	}
	reader := bufio.NewReaderSize(file, 16<<10)
	header, err := readYaCyProfileHeader(reader)
	if err != nil {
		return nil, "", err
	}
	profiles, err := readYaCyProfileRecords(reader)
	if err != nil {
		return nil, "", err
	}
	if len(profiles) == 0 {
		return nil, "", errors.New("YaCy profile sidecar contains no profiles")
	}
	return profiles, header.SourceID, nil
}

func readYaCyProfileHeader(reader *bufio.Reader) (frontierFileHeader, error) {
	line, err := readBoundedLine(reader)
	if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
		return frontierFileHeader{}, fmt.Errorf("read YaCy profile sidecar header: %w", err)
	}
	var header frontierFileHeader
	if json.Unmarshal(line, &header) != nil || header.Type != "yacy-crawl-profiles" ||
		header.Version != 1 || !validFrontierSourceID(header.SourceID) {
		return frontierFileHeader{}, errors.New("invalid versioned YaCy profile sidecar header")
	}
	return header, nil
}

func readYaCyProfileRecords(reader *bufio.Reader) (map[string]frontierSourceProfile, error) {
	profiles := make(map[string]frontierSourceProfile)
	for {
		line, err := readBoundedLine(reader)
		if errors.Is(err, io.EOF) && len(line) == 0 {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read YaCy profile sidecar record: %w", err)
		}
		var record yaCyProfileRecord
		if json.Unmarshal(line, &record) != nil || record.Type != "yacy-crawl-profile" ||
			record.Version != 1 || record.Handle == "" || len(record.Fields) == 0 {
			return nil, errors.New("invalid YaCy crawl-profile record")
		}
		if _, duplicate := profiles[record.Handle]; duplicate {
			return nil, errors.New("duplicate YaCy crawl-profile handle in sidecar")
		}
		profile, err := mapYaCyProfile(record)
		if err != nil {
			return nil, fmt.Errorf("map YaCy crawl profile: %w", err)
		}
		profiles[record.Handle] = frontierSourceProfile{
			sourceHandle: record.Handle,
			profile:      profile,
		}
	}
	return profiles, nil
}

func mapYaCyProfile(record yaCyProfileRecord) (yagocrawlcontract.CrawlProfile, error) {
	fields := record.Fields
	profile := yagocrawlcontract.CrawlProfile{
		Name:                 "Imported YaCy profile " + record.Handle,
		Scope:                yagocrawlcontract.ScopeWide,
		URLMustMatch:         mapYaCyPattern(fields["crawlerURLMustMatch"]),
		URLMustNotMatch:      fields["crawlerURLMustNotMatch"],
		IndexURLMustMatch:    mapYaCyPattern(fields["indexURLMustMatch"]),
		IndexURLMustNotMatch: fields["indexURLMustNotMatch"],
		MaxDepth:             yagocrawlcontract.MaxCrawlDepth,
		AllowQueryURLs:       true,
		MaxPagesPerHost:      yagocrawlcontract.UnlimitedPagesPerHost,
		Formats:              yagocrawlcontract.DefaultFormatToggles(),
	}
	maximumPagesPerRun := yagocrawlcontract.MaximumCrawlOrderImportBatchRecords
	profile.MaxPagesPerRun = &maximumPagesPerRun
	if err := applyYaCyProfileLimits(fields, &profile); err != nil {
		return yagocrawlcontract.CrawlProfile{}, err
	}
	if err := applyYaCyNoFollowSetting(fields, &profile); err != nil {
		return yagocrawlcontract.CrawlProfile{}, err
	}
	if err := validateYaCyRecrawlSetting(fields); err != nil {
		return yagocrawlcontract.CrawlProfile{}, err
	}
	metadata, err := mapYaCyProfileMetadata(record)
	if err != nil {
		return yagocrawlcontract.CrawlProfile{}, err
	}
	profile.SourceMetadata = yagocrawlcontract.NewSourceMetadata(metadata)
	profile = yagocrawlcontract.NewCrawlProfile(profile)
	if err := profile.Validate(); err != nil {
		return yagocrawlcontract.CrawlProfile{}, fmt.Errorf(
			"mapped YaCy crawl profile is not supported: %w",
			err,
		)
	}
	return profile, nil
}

func applyYaCyProfileLimits(
	fields map[string]string,
	profile *yagocrawlcontract.CrawlProfile,
) error {
	if raw := fields["generalDepth"]; raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return errors.New("invalid YaCy generalDepth")
		}
		if value > 0 {
			profile.MaxDepth = min(value, yagocrawlcontract.MaxCrawlDepth)
		}
	}
	if raw := fields["domMaxPages"]; raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return errors.New("invalid YaCy domMaxPages")
		}
		if value > 0 {
			profile.MaxPagesPerHost = value
		}
	}
	return nil
}

func applyYaCyNoFollowSetting(
	fields map[string]string,
	profile *yagocrawlcontract.CrawlProfile,
) error {
	if raw := fields["obeyHtmlRobotsNofollow"]; raw != "" {
		obey, err := strconv.ParseBool(raw)
		if err != nil {
			return errors.New("invalid YaCy robots-nofollow setting")
		}
		profile.FollowNoFollowLinks = !obey
	}
	return nil
}

func validateYaCyRecrawlSetting(fields map[string]string) error {
	if raw := fields["recrawlIfOlder"]; raw != "" && raw != "eternity" {
		// YaCy stores an absolute millisecond cutoff date here (Long.MAX_VALUE
		// means eternity); Yago stores a duration after fetch. Preserve the raw
		// source field below instead of inventing an interval conversion.
		cutoff, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || cutoff < 0 {
			return errors.New("invalid YaCy recrawl cutoff")
		}
	}
	return nil
}

func mapYaCyProfileMetadata(record yaCyProfileRecord) (map[string]string, error) {
	fields := record.Fields
	metadata := make(map[string]string, len(fields)+1)
	metadata["yacy.source_profile_handle"] = record.Handle
	for key, value := range fields {
		if len(key) > 240 || len(value) > 32<<10 {
			return nil, errors.New(
				"YaCy profile field exceeds migration bounds",
			)
		}
		metadata["yacy.profile."+key] = value
	}
	return metadata, nil
}

func mapYaCyPattern(pattern string) string {
	if pattern == "" {
		return yagocrawlcontract.MatchAll
	}
	return pattern
}

func validFrontierSourceID(value string) bool {
	if value == "" || len(value) > yagocrawlcontract.MaximumCrawlOrderImportSourceID {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

type frontierImportRunner struct {
	ctx          context.Context
	opts         frontierImportOptions
	progress     io.Writer
	input        *os.File
	inputInfo    os.FileInfo
	reader       *bufio.Reader
	profiles     map[string]frontierSourceProfile
	seen         map[string]struct{}
	endpoint     *url.URL
	statePath    string
	state        frontierImportCheckpoint
	client       *http.Client
	batch        []yagocrawlcontract.CrawlRequest
	batchEnd     int64
	batchProfile string
	batchRows    int64
	duplicates   int64
	blockedCreds int64
	blockedURLs  int64
	runRows      int
	offset       int64
}

func (r *frontierImportRunner) run() error {
	if r.state.Complete {
		return writeFrontierImportProgress(r.progress, r.state)
	}
	if err := r.runRecords(); err != nil {
		return err
	}
	if err := r.flush(); err != nil {
		return err
	}
	if !r.opts.DryRun {
		r.state.Complete = r.opts.MaxRecords == 0 && r.state.Offset == r.inputInfo.Size()
		if err := saveCheckpoint(r.statePath, r.state); err != nil {
			return err
		}
	}
	return writeFrontierImportSummary(r.progress, r.state, r.opts.DryRun)
}

func (r *frontierImportRunner) runRecords() error {
	for r.opts.MaxRecords == 0 || r.runRows < r.opts.MaxRecords {
		line, err := r.readLine()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := r.processLine(line); err != nil {
			return err
		}
	}
	return nil
}

func (r *frontierImportRunner) readLine() ([]byte, error) {
	line, err := readBoundedLine(r.reader)
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return nil, io.EOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read YaCy frontier at byte %d: %w", r.offset, err)
	}
	return line, nil
}

func (r *frontierImportRunner) processLine(line []byte) error {
	row, profile, err := r.decodeRow(line)
	if err != nil {
		return err
	}
	blockedReason := frontierURLBlockReason(row.URL)
	if blockedReason == "" && len(r.batch) > 0 && r.batchProfile != row.ProfileHandle {
		if err := r.flush(); err != nil {
			return err
		}
	}
	r.offset += int64(len(line))
	r.runRows++
	r.batchRows++
	r.batchEnd = r.offset
	identity := row.ProfileHandle + "\x00" + row.URL
	if blockedReason != "" {
		if err := r.recordBlocked(identity, blockedReason); err != nil {
			return err
		}
	} else if err := r.recordRequest(identity, row, profile); err != nil {
		return fmt.Errorf("map YaCy frontier record at byte %d: %w", r.batchEnd, err)
	}
	if len(r.batch) >= r.opts.BatchRecords {
		return r.flush()
	}
	return nil
}

func (r *frontierImportRunner) decodeRow(
	line []byte,
) (yaCyFrontierRecord, frontierSourceProfile, error) {
	var row yaCyFrontierRecord
	if json.Unmarshal(line, &row) != nil || row.Type != "crawlRequest" || row.Version != 1 ||
		row.Stack != "LOCAL" || row.URL == "" || row.ProfileHandle == "" {
		return row, frontierSourceProfile{}, fmt.Errorf(
			"invalid YaCy frontier record at byte %d",
			r.offset,
		)
	}
	profile, ok := r.profiles[row.ProfileHandle]
	if !ok {
		return row, frontierSourceProfile{}, fmt.Errorf(
			"YaCy frontier references an unavailable profile at byte %d",
			r.offset,
		)
	}
	return row, profile, nil
}

func (r *frontierImportRunner) recordBlocked(identity, reason string) error {
	if _, duplicate := r.seen[identity]; duplicate {
		r.duplicates++
		return nil
	}
	if err := r.markSeen(identity); err != nil {
		return err
	}
	if reason == "credential" {
		r.blockedCreds++
	} else {
		r.blockedURLs++
	}
	return nil
}

func (r *frontierImportRunner) recordRequest(
	identity string,
	row yaCyFrontierRecord,
	profile frontierSourceProfile,
) error {
	request, err := mapYaCyFrontierRequest(row, profile.profile)
	if err != nil {
		return err
	}
	if _, duplicate := r.seen[identity]; duplicate {
		r.duplicates++
		return nil
	}
	if err := r.markSeen(identity); err != nil {
		return err
	}
	r.batch = append(r.batch, request)
	if r.batchProfile == "" {
		r.batchProfile = row.ProfileHandle
	}
	return nil
}

func (r *frontierImportRunner) markSeen(identity string) error {
	if len(r.seen) >= maximumFrontierSeenURLs {
		return errors.New("frontier deduplication safety limit reached")
	}
	r.seen[identity] = struct{}{}
	return nil
}

func (r *frontierImportRunner) flush() error {
	if r.batchRows == 0 {
		return nil
	}
	importBatch, err := r.buildBatch()
	if err != nil {
		return err
	}
	if !r.opts.DryRun && len(r.batch) > 0 {
		if err := verifyImportFileUnchanged(r.input, r.inputInfo); err != nil {
			return err
		}
		response, err := sendFrontierImportBatch(
			r.ctx, r.client, r.endpoint.String(), r.opts.APIKey, importBatch,
		)
		if err != nil {
			return err
		}
		r.state.EnqueuedRequests += int64(response.EnqueuedRequests)
		r.state.AlreadyIndexed += int64(response.AlreadyIndexed)
	}
	if r.opts.DryRun {
		r.state.UniqueRequests += int64(len(r.batch))
	}
	r.recordBatchProgress()
	if !r.opts.DryRun {
		if err := saveCheckpoint(r.statePath, r.state); err != nil {
			return err
		}
		if err := writeFrontierImportProgress(r.progress, r.state); err != nil {
			return err
		}
	}
	r.resetBatch()
	return nil
}

func (r *frontierImportRunner) buildBatch() (yagocrawlcontract.CrawlOrderImportBatch, error) {
	if len(r.batch) == 0 {
		return yagocrawlcontract.CrawlOrderImportBatch{}, nil
	}
	batch := makeFrontierImportBatch(r.state.SourceID, r.batchProfile, r.profiles, r.batch, false)
	if err := batch.Validate(); err != nil {
		return yagocrawlcontract.CrawlOrderImportBatch{}, fmt.Errorf(
			"validate mapped frontier batch at byte %d: %w", r.batchEnd, err,
		)
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		return yagocrawlcontract.CrawlOrderImportBatch{}, fmt.Errorf(
			"size frontier import batch: %w",
			err,
		)
	}
	if len(encoded) > yagocrawlcontract.MaximumCrawlOrderImportBatchBytes {
		return yagocrawlcontract.CrawlOrderImportBatch{}, fmt.Errorf(
			"mapped frontier batch exceeds the %d-byte limit; rerun with a smaller --batch-records value",
			yagocrawlcontract.MaximumCrawlOrderImportBatchBytes,
		)
	}
	return batch, nil
}

func (r *frontierImportRunner) recordBatchProgress() {
	r.state.Offset = r.batchEnd
	r.state.Records += r.batchRows
	if !r.opts.DryRun {
		r.state.UniqueRequests += int64(len(r.batch))
	}
	r.state.DuplicateRequests += r.duplicates
	r.state.BlockedCredentialURLs += r.blockedCreds
	r.state.BlockedInvalidURLs += r.blockedURLs
}

func (r *frontierImportRunner) resetBatch() {
	r.batch = nil
	r.batchProfile = ""
	r.batchRows = 0
	r.duplicates = 0
	r.blockedCreds = 0
	r.blockedURLs = 0
}

func mapYaCyFrontierRequest(
	row yaCyFrontierRecord,
	profile yagocrawlcontract.CrawlProfile,
) (yagocrawlcontract.CrawlRequest, error) {
	if row.Depth < 0 || row.Depth > yagocrawlcontract.MaxCrawlDepth ||
		len(row.Anchor) > yagocrawlcontract.MaximumCrawlOrderImportAnchorBytes {
		return yagocrawlcontract.CrawlRequest{}, errors.New(
			"depth or anchor exceeds migration limit",
		)
	}
	request := yagocrawlcontract.CrawlRequest{
		URL: row.URL, Mode: yagocrawlcontract.CrawlRequestModeURL,
		AnchorName: row.Anchor, Depth: row.Depth, ProfileHandle: profile.Handle,
		SourceMetadata: yagocrawlcontract.NewSourceMetadata(map[string]string{
			"yacy.source_profile_handle": row.ProfileHandle,
			"yacy.stack":                 row.Stack,
			"yacy.queued_at_millis":      strconv.FormatInt(row.QueuedAtMillis, 10),
			"yacy.initiator_hex":         row.InitiatorHex,
		}),
	}
	if row.QueuedAtMillis > 0 {
		request.AppDate = time.UnixMilli(row.QueuedAtMillis).UTC()
	}
	if decoded, err := hex.DecodeString(row.InitiatorHex); err == nil {
		if initiator, err := yagomodel.ParseHash(string(decoded)); err == nil {
			request.Initiator = initiator
		}
	}
	return request, nil
}

func frontierURLBlockReason(raw string) string {
	if raw == "" || len(raw) > yagocrawlcontract.MaximumCrawlURLBytes || !utf8.ValidString(raw) {
		return "invalid"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Fragment != "" {
		return "invalid"
	}
	if parsed.User != nil {
		return "credential"
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ftp", "smb":
		return ""
	default:
		return "invalid"
	}
}

func makeFrontierImportBatch(
	sourceID string,
	sourceProfileHandle string,
	profiles map[string]frontierSourceProfile,
	requests []yagocrawlcontract.CrawlRequest,
	dryRun bool,
) yagocrawlcontract.CrawlOrderImportBatch {
	profile := profiles[sourceProfileHandle].profile
	batch := yagocrawlcontract.CrawlOrderImportBatch{
		Version: 1, SourceID: sourceID, Profile: profile,
		Requests: requests, DryRun: dryRun,
	}
	batch.BatchID, _ = batch.ComputeBatchID()
	return batch
}

func sendFrontierImportBatch(
	ctx context.Context,
	client *http.Client,
	endpoint, apiKey string,
	batch yagocrawlcontract.CrawlOrderImportBatch,
) (result frontierImportResponse, resultErr error) {
	encoded, err := json.Marshal(batch)
	if err != nil {
		return frontierImportResponse{}, fmt.Errorf("encode frontier import batch: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(encoded),
	)
	if err != nil {
		return frontierImportResponse{}, fmt.Errorf("create frontier import request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := client.Do(request)
	if err != nil {
		return frontierImportResponse{}, fmt.Errorf("send frontier import batch: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("close frontier import response: %w", err),
			)
		}
	}()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return frontierImportResponse{}, fmt.Errorf(
			"frontier import endpoint returned HTTP %d",
			response.StatusCode,
		)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return frontierImportResponse{}, fmt.Errorf("decode frontier import receipt: %w", err)
	}
	return result, nil
}

func rebuildFrontierSeen(
	input *os.File,
	end int64,
	profiles map[string]frontierSourceProfile,
) (map[string]struct{}, error) {
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek frontier before checkpoint replay: %w", err)
	}
	reader := bufio.NewReaderSize(input, 64<<10)
	_, headerEnd, err := readFrontierHeader(reader)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	position := headerEnd
	for position < end {
		line, err := readBoundedLine(reader)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read saved frontier before checkpoint: %w", err)
		}
		if len(line) == 0 {
			return nil, errors.New("checkpoint falls beyond frontier data")
		}
		position += int64(len(line))
		if position > end {
			return nil, errors.New("frontier checkpoint is not at a record boundary")
		}
		var row yaCyFrontierRecord
		if json.Unmarshal(line, &row) != nil || row.Type != "crawlRequest" || row.Version != 1 ||
			row.Stack != "LOCAL" {
			return nil, errors.New("invalid YaCy frontier record before checkpoint")
		}
		if _, ok := profiles[row.ProfileHandle]; !ok {
			return nil, errors.New("saved frontier references a missing YaCy profile")
		}
		identity := row.ProfileHandle + "\x00" + row.URL
		if len(seen) >= maximumFrontierSeenURLs {
			return nil, errors.New("frontier deduplication safety limit reached")
		}
		seen[identity] = struct{}{}
	}
	if position != end {
		return nil, errors.New("frontier checkpoint is not at a record boundary")
	}
	return seen, nil
}

type frontierCheckpointValidation struct {
	found        bool
	inputInfo    os.FileInfo
	profileInfo  os.FileInfo
	inputHash    string
	profilesHash string
	sourceID     string
	endpoint     *url.URL
	dryRun       bool
}

func validateFrontierCheckpoint(
	state frontierImportCheckpoint,
	validation frontierCheckpointValidation,
) error {
	if validation.dryRun || !validation.found {
		return nil
	}
	if state.Version != 1 || state.SourceID != validation.sourceID ||
		state.InputSHA256 != validation.inputHash ||
		state.ProfilesSHA256 != validation.profilesHash ||
		state.InputSize != validation.inputInfo.Size() ||
		state.InputModTime != validation.inputInfo.ModTime().UnixNano() ||
		state.ProfilesSize != validation.profileInfo.Size() ||
		state.ProfilesModTime != validation.profileInfo.ModTime().UnixNano() ||
		state.Offset < 0 || state.Offset > validation.inputInfo.Size() || validation.endpoint == nil ||
		state.DestinationFingerprint != destinationFingerprint(validation.endpoint) {
		return errors.New("frontier checkpoint does not match the input files or destination")
	}
	return nil
}

func loadFrontierImportCheckpoint(path string) (frontierImportCheckpoint, bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- selected by local migration operator.
	if errors.Is(err, os.ErrNotExist) {
		return frontierImportCheckpoint{}, false, nil
	}
	if err != nil {
		return frontierImportCheckpoint{}, false, fmt.Errorf("read frontier checkpoint: %w", err)
	}
	var state frontierImportCheckpoint
	if json.Unmarshal(data, &state) != nil || state.Version != 1 || state.SourceID == "" ||
		state.Offset < 0 {
		return frontierImportCheckpoint{}, false, errors.New("invalid frontier import checkpoint")
	}
	return state, true, nil
}

func hashOpenFile(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek file before hashing: %w", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash file contents: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind file after hashing: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeFrontierImportProgress(progress io.Writer, state frontierImportCheckpoint) error {
	_, err := fmt.Fprintf(
		progress,
		"frontier import checkpoint: records=%d unique=%d enqueued=%d already-indexed=%d duplicates=%d blocked-credential-urls=%d blocked-invalid-urls=%d offset=%d\n",
		state.Records,
		state.UniqueRequests,
		state.EnqueuedRequests,
		state.AlreadyIndexed,
		state.DuplicateRequests,
		state.BlockedCredentialURLs,
		state.BlockedInvalidURLs,
		state.Offset,
	)
	if err != nil {
		return fmt.Errorf("write frontier checkpoint progress: %w", err)
	}
	return nil
}

func writeFrontierImportSummary(
	progress io.Writer,
	state frontierImportCheckpoint,
	dryRun bool,
) error {
	if dryRun {
		_, err := fmt.Fprintf(
			progress,
			"dry-run: validated %d queue records; unique=%d duplicates=%d blocked-credential-urls=%d blocked-invalid-urls=%d; no API calls or checkpoint changes\n",
			state.Records,
			state.UniqueRequests,
			state.DuplicateRequests,
			state.BlockedCredentialURLs,
			state.BlockedInvalidURLs,
		)
		if err != nil {
			return fmt.Errorf("write frontier dry-run summary: %w", err)
		}
		return nil
	}
	return writeFrontierImportProgress(progress, state)
}
