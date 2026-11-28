package jenkins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ---- wire types (only what the tools need) ----

// BuildRef is a short build reference.
type BuildRef struct {
	Number    int    `json:"number"`
	URL       string `json:"url,omitempty"`
	Result    string `json:"result,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
}

// JobSummary is an entry in a folder listing.
type JobSummary struct {
	Class     string    `json:"_class"`
	Name      string    `json:"name"`
	FullName  string    `json:"fullName"`
	URL       string    `json:"url"`
	Color     string    `json:"color,omitempty"`
	Buildable bool      `json:"buildable"`
	LastBuild *BuildRef `json:"lastBuild,omitempty"`
}

// ParamDef is a job parameter definition.
type ParamDef struct {
	Class        string   `json:"_class,omitempty"`
	Name         string   `json:"name"`
	Type         string   `json:"type,omitempty"`
	Description  string   `json:"description,omitempty"`
	Choices      []string `json:"choices,omitempty"`
	DefaultValue *struct {
		Value any `json:"value"`
	} `json:"defaultParameterValue,omitempty"`
}

// Job is a job's details.
type Job struct {
	Class               string    `json:"_class"`
	Name                string    `json:"name"`
	FullName            string    `json:"fullName"`
	URL                 string    `json:"url"`
	Description         string    `json:"description,omitempty"`
	Color               string    `json:"color,omitempty"`
	Buildable           bool      `json:"buildable"`
	InQueue             bool      `json:"inQueue"`
	NextBuildNumber     int       `json:"nextBuildNumber,omitempty"`
	LastBuild           *BuildRef `json:"lastBuild,omitempty"`
	LastSuccessfulBuild *BuildRef `json:"lastSuccessfulBuild,omitempty"`
	LastFailedBuild     *BuildRef `json:"lastFailedBuild,omitempty"`
	LastCompletedBuild  *BuildRef `json:"lastCompletedBuild,omitempty"`
	HealthReport        []struct {
		Description string `json:"description"`
		Score       int    `json:"score"`
	} `json:"healthReport,omitempty"`
	Property []struct {
		ParameterDefinitions []ParamDef `json:"parameterDefinitions,omitempty"`
	} `json:"property,omitempty"`
	Jobs []JobSummary `json:"jobs,omitempty"`
}

// Build is one build's details.
type Build struct {
	Class             string `json:"_class,omitempty"`
	Number            int    `json:"number"`
	URL               string `json:"url"`
	DisplayName       string `json:"displayName,omitempty"`
	Description       string `json:"description,omitempty"`
	Result            string `json:"result,omitempty"`
	Building          bool   `json:"building"`
	Duration          int64  `json:"duration"`
	EstimatedDuration int64  `json:"estimatedDuration,omitempty"`
	Timestamp         int64  `json:"timestamp"`
	BuiltOn           string `json:"builtOn,omitempty"`
	Actions           []struct {
		Class      string `json:"_class,omitempty"`
		Parameters []struct {
			Name  string `json:"name"`
			Value any    `json:"value"`
		} `json:"parameters,omitempty"`
		Causes []struct {
			ShortDescription string `json:"shortDescription"`
			UserID           string `json:"userId,omitempty"`
			UserName         string `json:"userName,omitempty"`
		} `json:"causes,omitempty"`
		LastBuiltRevision *struct {
			SHA1   string `json:"SHA1"`
			Branch []struct {
				Name string `json:"name"`
			} `json:"branch"`
		} `json:"lastBuiltRevision,omitempty"`
		RemoteURLs []string `json:"remoteUrls,omitempty"`
	} `json:"actions,omitempty"`
	Artifacts  []Artifact `json:"artifacts,omitempty"`
	ChangeSets []struct {
		Items []struct {
			CommitID string `json:"commitId"`
			Msg      string `json:"msg"`
			Author   struct {
				FullName string `json:"fullName"`
			} `json:"author"`
		} `json:"items"`
	} `json:"changeSets,omitempty"`
}

// Artifact is an archived build artifact.
type Artifact struct {
	FileName     string `json:"fileName"`
	RelativePath string `json:"relativePath"`
}

// BuildSummary is one row of the build history.
type BuildSummary struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	DisplayName string `json:"displayName,omitempty"`
	Result      string `json:"result,omitempty"`
	Building    bool   `json:"building"`
	Timestamp   int64  `json:"timestamp"`
	Duration    int64  `json:"duration"`
}

// QueueItem is a queued build.
type QueueItem struct {
	ID           int64  `json:"id"`
	Why          string `json:"why,omitempty"`
	Blocked      bool   `json:"blocked"`
	Buildable    bool   `json:"buildable"`
	Stuck        bool   `json:"stuck"`
	InQueueSince int64  `json:"inQueueSince"`
	Params       string `json:"params,omitempty"`
	Task         struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"task"`
}

// WhoAmI is Jenkins' view of the caller.
type WhoAmI struct {
	Name          string   `json:"name"`
	Anonymous     bool     `json:"anonymous"`
	Authenticated bool     `json:"authenticated"`
	Authorities   []string `json:"authorities,omitempty"`
}

// ---- tree projections (keep responses small) ----

const (
	buildRefTree = "number,url,result,timestamp"
	jobsTree     = "jobs[_class,name,fullName,url,color,buildable,lastBuild[" + buildRefTree + "]]"
	jobTree      = "_class,name,fullName,url,description,color,buildable,inQueue,nextBuildNumber," +
		"lastBuild[" + buildRefTree + "],lastSuccessfulBuild[" + buildRefTree + "],lastFailedBuild[" + buildRefTree + "]," +
		"lastCompletedBuild[" + buildRefTree + "],healthReport[description,score]," +
		"property[parameterDefinitions[_class,name,type,description,choices,defaultParameterValue[value]]]," + jobsTree
	buildTree = "_class,number,url,displayName,description,result,building,duration,estimatedDuration,timestamp,builtOn," +
		"actions[_class,parameters[name,value],causes[shortDescription,userId,userName],lastBuiltRevision[SHA1,branch[name]],remoteUrls]," +
		"changeSets[items[commitId,msg,author[fullName]]],artifacts[fileName,relativePath]{0,200}"
	historyTreeFmt = "builds[number,url,displayName,result,building,timestamp,duration]{0,%d}"
	queueTree      = "items[id,why,blocked,buildable,stuck,inQueueSince,params,task[name,url]]"
)

// buildAliases are the permalinks Jenkins accepts instead of a build number.
var buildAliases = map[string]bool{
	"lastBuild": true, "lastSuccessfulBuild": true, "lastFailedBuild": true, "lastCompletedBuild": true,
	"lastStableBuild": true, "lastUnstableBuild": true, "lastUnsuccessfulBuild": true,
}

// ValidBuildRef reports whether ref is a positive build number or a permalink.
func ValidBuildRef(ref string) bool {
	if buildAliases[ref] {
		return true
	}
	n, err := strconv.Atoi(ref)

	return err == nil && n > 0
}

// ---- methods ----

// ListJobs lists the jobs and folders directly inside path ("" = base folder).
func (c *Client) ListJobs(ctx context.Context, path []string) ([]JobSummary, error) {
	u, err := c.jobURL(path, "api", "json")
	if err != nil {
		return nil, err
	}
	var out struct {
		Jobs []JobSummary `json:"jobs"`
	}
	if err := c.getJSON(ctx, u, jobsTree, &out); err != nil {
		return nil, err
	}

	return out.Jobs, nil
}

// GetJob returns job details. path must not be empty.
func (c *Client) GetJob(ctx context.Context, path []string) (*Job, error) {
	if len(path) == 0 {
		return nil, errors.New("job path is required")
	}
	u, err := c.jobURL(path, "api", "json")
	if err != nil {
		return nil, err
	}
	var j Job
	if err := c.getJSON(ctx, u, jobTree, &j); err != nil {
		return nil, err
	}

	return &j, nil
}

// GetBuild returns details of build ref (number or permalink).
func (c *Client) GetBuild(ctx context.Context, path []string, ref string) (*Build, error) {
	if len(path) == 0 || !ValidBuildRef(ref) {
		return nil, fmt.Errorf("invalid job path or build %q", ref)
	}
	u, err := c.jobURL(path, ref, "api", "json")
	if err != nil {
		return nil, err
	}
	var b Build
	if err := c.getJSON(ctx, u, buildTree, &b); err != nil {
		return nil, err
	}

	return &b, nil
}

// BuildHistory returns the newest limit builds of a job.
func (c *Client) BuildHistory(ctx context.Context, path []string, limit int) ([]BuildSummary, error) {
	if len(path) == 0 {
		return nil, errors.New("job path is required")
	}
	u, err := c.jobURL(path, "api", "json")
	if err != nil {
		return nil, err
	}
	var out struct {
		Builds []BuildSummary `json:"builds"`
	}
	if err := c.getJSON(ctx, u, fmt.Sprintf(historyTreeFmt, limit), &out); err != nil {
		return nil, err
	}

	return out.Builds, nil
}

// Console is a chunk of build console output.
type Console struct {
	Text      string // the chunk
	Start     int64  // offset of Text in the full log
	NextStart int64  // offset to continue from
	Size      int64  // log size known to Jenkins at request time
	MoreData  bool   // build still running, or more text after NextStart
	Truncated bool   // the chunk was cut at maxBytes
}

// ConsoleText reads up to maxBytes of console output. start < 0 means "the
// last maxBytes" (tail).
func (c *Client) ConsoleText(ctx context.Context, path []string, ref string, start int64, maxBytes int) (*Console, error) {
	if len(path) == 0 || !ValidBuildRef(ref) {
		return nil, fmt.Errorf("invalid job path or build %q", ref)
	}
	if start < 0 {
		// Jenkins restarts from 0 when start is past the end, so learn the
		// size first (headers only), then fetch just the tail.
		size, err := c.progressive(ctx, path, ref, 0, 0)
		if err != nil {
			return nil, err
		}
		start = max(size.Size-int64(maxBytes), 0)
	}
	return c.progressive(ctx, path, ref, start, maxBytes)
}

func (c *Client) progressive(ctx context.Context, path []string, ref string, start int64, maxBytes int) (*Console, error) {
	u, err := c.jobURL(path, ref, "logText", "progressiveText")
	if err != nil {
		return nil, err
	}
	u.RawQuery = url.Values{"start": {strconv.FormatInt(start, 10)}}.Encode()
	resp, err := c.send(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	con := &Console{Start: start, MoreData: resp.Header.Get("X-More-Data") == "true"}
	if s, err := strconv.ParseInt(resp.Header.Get("X-Text-Size"), 10, 64); err == nil {
		con.Size = s
	}
	if maxBytes <= 0 {
		return con, nil // headers only
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read console output: %w", err)
	}
	if len(b) > maxBytes {
		b = trimPartialRune(b[:maxBytes])
		con.Truncated, con.MoreData = true, true
	}
	con.Text = strings.ToValidUTF8(string(b), "�")
	con.NextStart = start + int64(len(b))
	if !con.Truncated && con.Size > con.NextStart {
		con.NextStart = con.Size
	}

	return con, nil
}

// trimPartialRune drops an incomplete UTF-8 sequence at the end of b, so the
// next chunk (from NextStart) starts on a character boundary.
func trimPartialRune(b []byte) []byte {
	for k := len(b) - 1; k >= 0 && k >= len(b)-utf8.UTFMax; k-- {
		if utf8.RuneStart(b[k]) {
			if !utf8.FullRune(b[k:]) {
				return b[:k]
			}

			break
		}
	}

	return b
}

// GetArtifact reads up to maxBytes of an archived artifact of a build.
// relativePath comes from Build.Artifacts; every segment is escaped.
func (c *Client) GetArtifact(ctx context.Context, path []string, ref, relativePath string, maxBytes int) ([]byte, bool, error) {
	if len(path) == 0 || !ValidBuildRef(ref) {
		return nil, false, fmt.Errorf("invalid job path or build %q", ref)
	}
	segs := append([]string{ref, "artifact"}, strings.Split(relativePath, "/")...)
	for _, s := range segs[2:] {
		if s == "" || s == "." || s == ".." {
			return nil, false, fmt.Errorf("invalid artifact path %q", relativePath)
		}
	}
	u, err := c.jobURL(path, segs...)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.send(ctx, u)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, false, fmt.Errorf("read artifact: %w", err)
	}
	if len(b) > maxBytes {
		return b[:maxBytes], true, nil
	}

	return b, false, nil
}

// Queue returns queued items; with a base folder, only items inside it.
func (c *Client) Queue(ctx context.Context) ([]QueueItem, error) {
	u := c.base.JoinPath("queue", "api", "json")
	var out struct {
		Items []QueueItem `json:"items"`
	}
	if err := c.getJSON(ctx, u, queueTree, &out); err != nil {
		return nil, err
	}
	if len(c.folder) == 0 {
		return out.Items, nil
	}
	scope, err := c.jobURL(nil)
	if err != nil {
		return nil, err
	}
	items := out.Items[:0]
	for _, it := range out.Items {
		if tu, err := url.Parse(it.Task.URL); err == nil && strings.HasPrefix(tu.EscapedPath(), scope.EscapedPath()) {
			items = append(items, it)
		}
	}

	return items, nil
}

// WhoAmI returns who Jenkins thinks the caller is (credential check).
func (c *Client) WhoAmI(ctx context.Context) (*WhoAmI, error) {
	var w WhoAmI
	if err := c.getJSON(ctx, c.base.JoinPath("whoAmI", "api", "json"), "", &w); err != nil {
		return nil, err
	}

	return &w, nil
}

// RelPath converts a Jenkins fullName into a path relative to the base folder.
func (c *Client) RelPath(fullName string) string { return c.relPath(fullName) }
