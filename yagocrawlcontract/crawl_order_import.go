package yagocrawlcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	MaximumCrawlOrderImportBatchRecords  = 256
	MaximumCrawlOrderImportBatchBytes    = 4 << 20
	MaximumCrawlOrderImportSourceID      = 96
	MaximumCrawlOrderImportAnchorBytes   = 16 << 10
	MaximumCrawlOrderImportMetadata      = 64
	MaximumCrawlOrderImportMetadataBytes = 1 << 20
)

// CrawlOrderImportBatch is the bounded, idempotent admin interchange for
// recovering a source crawler's pending frontier. Source-specific provenance
// belongs in CrawlProfile.SourceMetadata and CrawlRequest.SourceMetadata.
type CrawlOrderImportBatch struct {
	Version  int            `json:"version"`
	SourceID string         `json:"sourceId"`
	BatchID  string         `json:"batchId"`
	Profile  CrawlProfile   `json:"profile"`
	Requests []CrawlRequest `json:"requests"`
	DryRun   bool           `json:"dryRun,omitempty"`
}

// ComputeBatchID returns the stable content identity used to make retries
// idempotent. DryRun and SourceID are deliberately excluded: the former must
// not change the identity of a later real import, and the latter namespaces
// the same payload when it comes from distinct exports.
func (b CrawlOrderImportBatch) ComputeBatchID() (string, error) {
	payload := struct {
		Profile  CrawlProfile
		Requests []CrawlRequest
	}{
		Profile:  b.Profile,
		Requests: b.Requests,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal crawl-order import batch identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// IdempotencyKey namespaces an accepted batch within its source export.
func (b CrawlOrderImportBatch) IdempotencyKey() string {
	return "frontier:" + b.SourceID + ":" + b.BatchID
}

// Validate checks the bounded, versioned frontier-import envelope before it
// can be persisted as a crawl order.
func (b CrawlOrderImportBatch) Validate() error {
	if err := validateCrawlOrderImportBounds(b); err != nil {
		return err
	}
	if err := validateCrawlOrderImportSource(b.SourceID); err != nil {
		return err
	}
	if err := validateCrawlOrderImportProfile(b.Profile); err != nil {
		return err
	}
	if err := validateCrawlOrderImportRequests(b.Profile.Handle, b.Requests); err != nil {
		return err
	}
	return validateCrawlOrderImportIdentity(b)
}

func validateCrawlOrderImportBounds(batch CrawlOrderImportBatch) error {
	if batch.Version != 1 || len(batch.Requests) == 0 ||
		len(batch.Requests) > MaximumCrawlOrderImportBatchRecords {
		return fmt.Errorf("unsupported frontier version or request count")
	}
	return nil
}

func validateCrawlOrderImportSource(sourceID string) error {
	if len(sourceID) == 0 || len(sourceID) > MaximumCrawlOrderImportSourceID ||
		strings.TrimSpace(sourceID) != sourceID || !validImportSourceID(sourceID) {
		return fmt.Errorf("invalid frontier source ID")
	}
	return nil
}

func validateCrawlOrderImportProfile(profile CrawlProfile) error {
	if len(profile.Handle) == 0 || len(profile.Handle) > MaximumProfileHandleBytes ||
		!utf8.ValidString(profile.Name) || len(profile.Name) > 512 {
		return fmt.Errorf("invalid frontier crawl profile")
	}
	if err := profile.Validate(); err != nil {
		return fmt.Errorf("invalid frontier crawl profile: %w", err)
	}
	if err := validateImportMetadata(profile.SourceMetadata); err != nil {
		return fmt.Errorf("invalid frontier profile metadata: %w", err)
	}
	return nil
}

func validateCrawlOrderImportRequests(
	profileHandle string,
	requests []CrawlRequest,
) error {
	seen := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		if err := validateCrawlOrderImportRequest(profileHandle, request); err != nil {
			return err
		}
		identity := request.ProfileHandle + "\x00" + request.URL
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("frontier batch contains duplicate URL/profile pairs")
		}
		seen[identity] = struct{}{}
		if err := validateImportMetadata(request.SourceMetadata); err != nil {
			return fmt.Errorf("invalid frontier request metadata: %w", err)
		}
	}
	return nil
}

func validateCrawlOrderImportRequest(profileHandle string, request CrawlRequest) error {
	if err := validateCrawlOrderImportURL(request.URL); err != nil {
		return err
	}
	if request.ProfileHandle != profileHandle || request.Depth < 0 ||
		request.Depth > MaxCrawlDepth || len(request.AnchorName) > MaximumCrawlOrderImportAnchorBytes ||
		!utf8.ValidString(request.AnchorName) {
		return fmt.Errorf("invalid frontier request metadata")
	}
	if request.Mode != "" {
		if _, ok := NormalizeCrawlRequestMode(request.Mode); !ok {
			return fmt.Errorf("invalid frontier request mode")
		}
	}
	if err := validateCrawlOrderImportReferrer(request.ReferrerURL); err != nil {
		return err
	}
	if request.Initiator != "" && !request.Initiator.Valid() {
		return fmt.Errorf("invalid frontier initiator")
	}
	return nil
}

func validateCrawlOrderImportURL(raw string) error {
	if raw == "" || len(raw) > MaximumCrawlURLBytes || !utf8.ValidString(raw) {
		return fmt.Errorf("invalid frontier URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" ||
		!importableCrawlScheme(parsed.Scheme) {
		return fmt.Errorf("frontier URL must be an absolute HTTP, HTTPS, FTP, or SMB URL")
	}
	return nil
}

func validateCrawlOrderImportReferrer(raw string) error {
	if raw == "" {
		return nil
	}
	referrer, err := url.Parse(raw)
	if err != nil || referrer.Host == "" || referrer.User != nil ||
		referrer.Fragment != "" || !importableCrawlScheme(referrer.Scheme) {
		return fmt.Errorf("invalid frontier referrer URL")
	}
	return nil
}

func validateCrawlOrderImportIdentity(batch CrawlOrderImportBatch) error {
	if len(batch.BatchID) != sha256.Size*2 {
		return fmt.Errorf("invalid frontier batch ID")
	}
	if _, err := hex.DecodeString(batch.BatchID); err != nil ||
		strings.ToLower(batch.BatchID) != batch.BatchID {
		return fmt.Errorf("invalid frontier batch ID")
	}
	computed, err := batch.ComputeBatchID()
	if err != nil {
		return err
	}
	if computed != batch.BatchID {
		return fmt.Errorf("frontier batch ID does not match payload")
	}
	return nil
}

func validImportSourceID(value string) bool {
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func importableCrawlScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "http", "https", "ftp", "smb":
		return true
	default:
		return false
	}
}

func validateImportMetadata(metadata *SourceMetadata) error {
	if metadata == nil {
		return nil
	}
	if len(*metadata) > MaximumCrawlOrderImportMetadata {
		return fmt.Errorf("too many metadata fields")
	}
	bytes := 0
	for key, value := range *metadata {
		if key == "" || len(key) > 256 || !utf8.ValidString(key) || !utf8.ValidString(value) {
			return fmt.Errorf("invalid metadata entry")
		}
		bytes += len(key) + len(value)
	}
	if bytes > MaximumCrawlOrderImportMetadataBytes {
		return fmt.Errorf("metadata exceeds size limit")
	}
	return nil
}
