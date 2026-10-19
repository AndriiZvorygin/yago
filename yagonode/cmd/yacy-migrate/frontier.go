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

func importFrontier(ctx context.Context, opts frontierImportOptions, progress io.Writer) error {
	if err := validateFrontierImportOptions(opts); err != nil {
		return err
	}
	input, err := os.Open(opts.Input) // #nosec G304 -- selected by local migration operator.
	if err != nil {
		return fmt.Errorf("open YaCy frontier: %w", err)
	}
	defer closeImportInput(input, "close YaCy frontier", nil)
	inputInfo, err := input.Stat()
	if err != nil {
		return fmt.Errorf("stat YaCy frontier: %w", err)
	}
	profileFile, err := os.Open(
		opts.Profiles,
	) // #nosec G304 -- selected by local migration operator.
	if err != nil {
		return fmt.Errorf("open YaCy profile sidecar: %w", err)
	}
	defer closeImportInput(profileFile, "close YaCy profile sidecar", nil)
	profileInfo, err := profileFile.Stat()
	if err != nil {
		return fmt.Errorf("stat YaCy profile sidecar: %w", err)
	}
	profilesHash, err := hashOpenFile(profileFile)
	if err != nil {
		return fmt.Errorf("fingerprint YaCy profile sidecar: %w", err)
	}
	profiles, profileSourceID, err := loadYaCyProfiles(profileFile)
	if err != nil {
		return err
	}
	inputHash, err := hashOpenFile(input)
	if err != nil {
		return fmt.Errorf("fingerprint YaCy frontier: %w", err)
	}
	endpoint, err := validateFrontierEndpoint(opts.Endpoint, opts.DryRun)
	if err != nil {
		return err
	}
	statePath := opts.State
	if statePath == "" {
		statePath = filepath.Clean(opts.Input) + ".frontier.import.state.json"
	}
	state, found, err := loadFrontierImportCheckpoint(statePath)
	if err != nil {
		return err
	}
	if err := validateFrontierCheckpoint(
		state, found, inputInfo, profileInfo, inputHash, profilesHash,
		profileSourceID, endpoint, opts.DryRun,
	); err != nil {
		return err
	}
	if opts.DryRun {
		found = false
		state = frontierImportCheckpoint{}
	}
	reader := bufio.NewReaderSize(input, 64<<10)
	header, headerEnd, err := readFrontierHeader(reader)
	if err != nil {
		return err
	}
	if header.SourceID != profileSourceID {
		return errors.New("YaCy frontier and profile sidecar source IDs do not match")
	}
	if !found {
		state = frontierImportCheckpoint{
			Version: 1, SourceID: header.SourceID, InputSHA256: inputHash,
			ProfilesSHA256: profilesHash, InputSize: inputInfo.Size(),
			InputModTime: inputInfo.ModTime().UnixNano(), ProfilesSize: profileInfo.Size(),
			ProfilesModTime: profileInfo.ModTime().UnixNano(), Offset: headerEnd,
		}
		if !opts.DryRun {
			state.DestinationFingerprint = destinationFingerprint(endpoint)
			if err := saveCheckpoint(statePath, state); err != nil {
				return err
			}
		}
	} else if state.Offset > headerEnd {
		seen, err := rebuildFrontierSeen(input, state.Offset, profiles)
		if err != nil {
			return fmt.Errorf("restore frontier deduplication state: %w", err)
		}
		if _, err := input.Seek(state.Offset, io.SeekStart); err != nil {
			return fmt.Errorf("seek YaCy frontier checkpoint: %w", err)
		}
		reader.Reset(input)
		return runFrontierImport(
			ctx,
			opts,
			progress,
			input,
			inputInfo,
			reader,
			profiles,
			seen,
			endpoint,
			statePath,
			state,
		)
	} else {
		if _, err := input.Seek(headerEnd, io.SeekStart); err != nil {
			return fmt.Errorf("seek YaCy frontier header: %w", err)
		}
		reader.Reset(input)
	}
	return runFrontierImport(
		ctx, opts, progress, input, inputInfo, reader, profiles,
		make(map[string]struct{}), endpoint, statePath, state,
	)
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
	line, err := readBoundedLine(reader)
	if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
		return nil, "", fmt.Errorf("read YaCy profile sidecar header: %w", err)
	}
	var header frontierFileHeader
	if json.Unmarshal(line, &header) != nil || header.Type != "yacy-crawl-profiles" ||
		header.Version != 1 || !validFrontierSourceID(header.SourceID) {
		return nil, "", errors.New("invalid versioned YaCy profile sidecar header")
	}
	profiles := make(map[string]frontierSourceProfile)
	for {
		line, err = readBoundedLine(reader)
		if errors.Is(err, io.EOF) && len(line) == 0 {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, "", fmt.Errorf("read YaCy profile sidecar record: %w", err)
		}
		var record yaCyProfileRecord
		if json.Unmarshal(line, &record) != nil || record.Type != "yacy-crawl-profile" ||
			record.Version != 1 || record.Handle == "" || len(record.Fields) == 0 {
			return nil, "", errors.New("invalid YaCy crawl-profile record")
		}
		if _, duplicate := profiles[record.Handle]; duplicate {
			return nil, "", errors.New("duplicate YaCy crawl-profile handle in sidecar")
		}
		profile, err := mapYaCyProfile(record)
		if err != nil {
			return nil, "", fmt.Errorf("map YaCy crawl profile: %w", err)
		}
		profiles[record.Handle] = frontierSourceProfile{
			sourceHandle: record.Handle,
			profile:      profile,
		}
	}
	if len(profiles) == 0 {
		return nil, "", errors.New("YaCy profile sidecar contains no profiles")
	}
	return profiles, header.SourceID, nil
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
	if raw := fields["generalDepth"]; raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return yagocrawlcontract.CrawlProfile{}, errors.New("invalid YaCy generalDepth")
		}
		if value > 0 {
			profile.MaxDepth = min(value, yagocrawlcontract.MaxCrawlDepth)
		}
	}
	if raw := fields["domMaxPages"]; raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return yagocrawlcontract.CrawlProfile{}, errors.New("invalid YaCy domMaxPages")
		}
		if value > 0 {
			profile.MaxPagesPerHost = value
		}
	}
	if raw := fields["obeyHtmlRobotsNofollow"]; raw != "" {
		obey, err := strconv.ParseBool(raw)
		if err != nil {
			return yagocrawlcontract.CrawlProfile{}, errors.New(
				"invalid YaCy robots-nofollow setting",
			)
		}
		profile.FollowNoFollowLinks = !obey
	}
	if raw := fields["recrawlIfOlder"]; raw != "" && raw != "eternity" {
		// YaCy stores an absolute millisecond cutoff date here (Long.MAX_VALUE
		// means eternity); Yago stores a duration after fetch. Preserve the raw
		// source field below instead of inventing an interval conversion.
		cutoff, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || cutoff < 0 {
			return yagocrawlcontract.CrawlProfile{}, errors.New("invalid YaCy recrawl cutoff")
		}
	}
	metadata := make(map[string]string, len(fields)+1)
	metadata["yacy.source_profile_handle"] = record.Handle
	for key, value := range fields {
		if len(key) > 240 || len(value) > 32<<10 {
			return yagocrawlcontract.CrawlProfile{}, errors.New(
				"YaCy profile field exceeds migration bounds",
			)
		}
		metadata["yacy.profile."+key] = value
	}
	profile.SourceMetadata = metadata
	profile = yagocrawlcontract.NewCrawlProfile(profile)
	if err := profile.Validate(); err != nil {
		return yagocrawlcontract.CrawlProfile{}, fmt.Errorf(
			"mapped YaCy crawl profile is not supported: %w",
			err,
		)
	}
	return profile, nil
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

func runFrontierImport(
	ctx context.Context,
	opts frontierImportOptions,
	progress io.Writer,
	input *os.File,
	inputInfo os.FileInfo,
	reader *bufio.Reader,
	profiles map[string]frontierSourceProfile,
	seen map[string]struct{},
	endpoint *url.URL,
	statePath string,
	state frontierImportCheckpoint,
) error {
	if state.Complete {
		return writeFrontierImportProgress(progress, state)
	}
	client := &http.Client{Timeout: 90 * time.Second}
	var batch []yagocrawlcontract.CrawlRequest
	batchEnd := state.Offset
	batchProfileHandle := ""
	var batchRows, batchDuplicates, batchBlockedCredentialURLs, batchBlockedInvalidURLs int64
	var runRows int
	offset := state.Offset
	flush := func() error {
		if batchRows == 0 {
			return nil
		}
		var importBatch yagocrawlcontract.CrawlOrderImportBatch
		if len(batch) > 0 {
			importBatch = makeFrontierImportBatch(
				state.SourceID, batchProfileHandle, profiles, batch, false,
			)
			if err := importBatch.Validate(); err != nil {
				return fmt.Errorf("validate mapped frontier batch at byte %d: %w", batchEnd, err)
			}
			encoded, err := json.Marshal(importBatch)
			if err != nil {
				return fmt.Errorf("size frontier import batch: %w", err)
			}
			if len(encoded) > yagocrawlcontract.MaximumCrawlOrderImportBatchBytes {
				return fmt.Errorf(
					"mapped frontier batch exceeds the %d-byte limit; rerun with a smaller --batch-records value",
					yagocrawlcontract.MaximumCrawlOrderImportBatchBytes,
				)
			}
		}
		if !opts.DryRun && len(batch) > 0 {
			if err := verifyImportFileUnchanged(input, inputInfo); err != nil {
				return err
			}
			response, err := sendFrontierImportBatch(
				ctx,
				client,
				endpoint.String(),
				opts.APIKey,
				importBatch,
			)
			if err != nil {
				return err
			}
			state.EnqueuedRequests += int64(response.EnqueuedRequests)
			state.AlreadyIndexed += int64(response.AlreadyIndexed)
		} else if opts.DryRun {
			state.UniqueRequests += int64(len(batch))
		}
		if opts.DryRun {
			state.Offset = batchEnd
			state.Records += batchRows
			state.DuplicateRequests += batchDuplicates
			state.BlockedCredentialURLs += batchBlockedCredentialURLs
			state.BlockedInvalidURLs += batchBlockedInvalidURLs
			batch = nil
			batchRows, batchDuplicates, batchBlockedCredentialURLs, batchBlockedInvalidURLs = 0, 0, 0, 0
			batchProfileHandle = ""
			return nil
		}
		state.Offset = batchEnd
		state.Records += batchRows
		state.UniqueRequests += int64(len(batch))
		state.DuplicateRequests += batchDuplicates
		state.BlockedCredentialURLs += batchBlockedCredentialURLs
		state.BlockedInvalidURLs += batchBlockedInvalidURLs
		if err := saveCheckpoint(statePath, state); err != nil {
			return err
		}
		if err := writeFrontierImportProgress(progress, state); err != nil {
			return err
		}
		batch = nil
		batchRows, batchDuplicates, batchBlockedCredentialURLs, batchBlockedInvalidURLs = 0, 0, 0, 0
		batchProfileHandle = ""
		return nil
	}
	for {
		if opts.MaxRecords > 0 && runRows >= opts.MaxRecords {
			break
		}
		line, readErr := readBoundedLine(reader)
		if errors.Is(readErr, io.EOF) && len(line) == 0 {
			break
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read YaCy frontier at byte %d: %w", offset, readErr)
		}
		var row yaCyFrontierRecord
		if json.Unmarshal(line, &row) != nil || row.Type != "crawlRequest" || row.Version != 1 ||
			row.Stack != "LOCAL" || row.URL == "" || row.ProfileHandle == "" {
			return fmt.Errorf("invalid YaCy frontier record at byte %d", offset)
		}
		profile, ok := profiles[row.ProfileHandle]
		if !ok {
			return fmt.Errorf("YaCy frontier references an unavailable profile at byte %d", offset)
		}
		blockedReason := frontierURLBlockReason(row.URL)
		if blockedReason == "" && len(batch) > 0 && batchProfileHandle != row.ProfileHandle {
			if err := flush(); err != nil {
				return err
			}
		}
		offset += int64(len(line))
		lineEnd := offset
		runRows++
		batchRows++
		batchEnd = lineEnd
		identity := row.ProfileHandle + "\x00" + row.URL
		if blockedReason != "" {
			if _, duplicate := seen[identity]; duplicate {
				batchDuplicates++
			} else {
				if len(seen) >= maximumFrontierSeenURLs {
					return errors.New("frontier deduplication safety limit reached")
				}
				seen[identity] = struct{}{}
				if blockedReason == "credential" {
					batchBlockedCredentialURLs++
				} else {
					batchBlockedInvalidURLs++
				}
			}
			if len(batch) >= opts.BatchRecords {
				if err := flush(); err != nil {
					return err
				}
			}
			continue
		}
		request, err := mapYaCyFrontierRequest(row, profile.profile)
		if err != nil {
			return fmt.Errorf("map YaCy frontier record at byte %d: %w", lineEnd, err)
		}
		if _, duplicate := seen[identity]; duplicate {
			batchDuplicates++
		} else {
			if len(seen) >= maximumFrontierSeenURLs {
				return errors.New("frontier deduplication safety limit reached")
			}
			seen[identity] = struct{}{}
			batch = append(batch, request)
			if batchProfileHandle == "" {
				batchProfileHandle = row.ProfileHandle
			}
		}
		if len(batch) >= opts.BatchRecords {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if batchRows > 0 {
		if err := flush(); err != nil {
			return err
		}
	}
	if !opts.DryRun {
		state.Complete = opts.MaxRecords == 0 && state.Offset == inputInfo.Size()
		if err := saveCheckpoint(statePath, state); err != nil {
			return err
		}
	}
	return writeFrontierImportSummary(progress, state, opts.DryRun)
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
		SourceMetadata: map[string]string{
			"yacy.source_profile_handle": row.ProfileHandle,
			"yacy.stack":                 row.Stack,
			"yacy.queued_at_millis":      strconv.FormatInt(row.QueuedAtMillis, 10),
			"yacy.initiator_hex":         row.InitiatorHex,
		},
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
) (frontierImportResponse, error) {
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
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return frontierImportResponse{}, fmt.Errorf(
			"frontier import endpoint returned HTTP %d",
			response.StatusCode,
		)
	}
	var result frontierImportResponse
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
		return nil, err
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

func validateFrontierCheckpoint(
	state frontierImportCheckpoint,
	found bool,
	inputInfo, profileInfo os.FileInfo,
	inputHash, profilesHash, sourceID string,
	endpoint *url.URL,
	dryRun bool,
) error {
	if dryRun {
		return nil
	}
	if !found {
		return nil
	}
	if dryRun || state.Version != 1 || state.SourceID != sourceID ||
		state.InputSHA256 != inputHash || state.ProfilesSHA256 != profilesHash ||
		state.InputSize != inputInfo.Size() || state.InputModTime != inputInfo.ModTime().UnixNano() ||
		state.ProfilesSize != profileInfo.Size() || state.ProfilesModTime != profileInfo.ModTime().UnixNano() ||
		state.Offset < 0 || state.Offset > inputInfo.Size() || endpoint == nil ||
		state.DestinationFingerprint != destinationFingerprint(endpoint) {
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
		return "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
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
	return err
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
		return err
	}
	return writeFrontierImportProgress(progress, state)
}
