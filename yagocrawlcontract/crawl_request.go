package yagocrawlcontract

import (
	"time"

	"github.com/D4rk4/yago/yagomodel"
)

type CrawlRequest struct {
	URL           string
	Mode          CrawlRequestMode
	ReferrerURL   string
	AnchorName    string
	Depth         int
	ProfileHandle string
	Initiator     yagomodel.Hash
	AppDate       time.Time
	LastModified  time.Time
	// SourceMetadata preserves adapter-specific crawl-state fields that Yago
	// does not interpret. It is carried with the durable order for audit and
	// future migrations; ordinary crawl behavior ignores it.
	SourceMetadata map[string]string `json:",omitempty"`
}

type CrawlRequestMode string

const (
	CrawlRequestModeURL      CrawlRequestMode = "url"
	CrawlRequestModeSitemap  CrawlRequestMode = "sitemap"
	CrawlRequestModeSitelist CrawlRequestMode = "sitelist"
	CrawlRequestModeRobots   CrawlRequestMode = "robots"
)

func NormalizeCrawlRequestMode(mode CrawlRequestMode) (CrawlRequestMode, bool) {
	switch mode {
	case "", CrawlRequestModeURL:
		return CrawlRequestModeURL, true
	case CrawlRequestModeSitemap, CrawlRequestModeSitelist, CrawlRequestModeRobots:
		return mode, true
	default:
		return "", false
	}
}
