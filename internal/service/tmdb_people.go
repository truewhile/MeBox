package service

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	tmdbPersonIDPrefix    = "person~tmdb~"
	tmdbProfileTagPrefix  = "tmdb:"
	tmdbProfileTagVersion = "p2"
	tmdbPeopleMax         = 80
)

var tmdbProfilePathRE = regexp.MustCompile(`^/[A-Za-z0-9._-]+$`)

type tmdbCreditCast struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Character   string `json:"character"`
	ProfilePath string `json:"profile_path"`
	Order       int    `json:"order"`
	Roles       []struct {
		Character string `json:"character"`
	} `json:"roles"`
}

type tmdbCreditCrew struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Job         string `json:"job"`
	Department  string `json:"department"`
	ProfilePath string `json:"profile_path"`
	Jobs        []struct {
		Job string `json:"job"`
	} `json:"jobs"`
}

type tmdbCreditsResponse struct {
	Cast []tmdbCreditCast `json:"cast"`
	Crew []tmdbCreditCrew `json:"crew"`
}

// GetPeople fetches cast/crew for a movie or series from TMDb. Nothing is
// persisted; callers should keep the returned entries in a short-lived cache.
func (t *TMDbProvider) GetPeople(ctx context.Context, tmdbID int, mediaType string) ([]map[string]any, error) {
	if t == nil || t.cfg == nil || tmdbID <= 0 {
		return nil, nil
	}
	apiKey := t.resolveAPIKey(ctx)
	if strings.TrimSpace(apiKey) == "" {
		return nil, nil
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	endpoint := "/movie/" + strconv.Itoa(tmdbID) + "/credits"
	if mediaType == "tv" {
		endpoint = "/tv/" + strconv.Itoa(tmdbID) + "/aggregate_credits"
	}
	q := url.Values{}
	q.Set("api_key", apiKey)
	q.Set("language", "zh-CN")
	var out tmdbCreditsResponse
	if err := t.getJSON(ctx, t.resolveBaseURL(ctx)+endpoint+"?"+q.Encode(), &out); err != nil {
		return nil, err
	}

	people := make([]map[string]any, 0, 32)
	seen := map[string]int{}
	for _, cast := range out.Cast {
		name := strings.TrimSpace(cast.Name)
		if name == "" || cast.ID <= 0 {
			continue
		}
		role := strings.TrimSpace(cast.Character)
		if role == "" {
			for _, item := range cast.Roles {
				if role = strings.TrimSpace(item.Character); role != "" {
					break
				}
			}
		}
		if role == "" {
			role = "Actor"
		}
		person := newTMDbPerson(cast.ID, name, "Actor", role, cast.ProfilePath)
		key := personIdentity(person)
		if idx, ok := seen[key]; ok {
			mergePersonRole(people[idx], role)
			continue
		}
		seen[key] = len(people)
		people = append(people, person)
		if len(people) >= tmdbPeopleMax {
			break
		}
	}

	for _, crew := range out.Crew {
		name := strings.TrimSpace(crew.Name)
		if name == "" || crew.ID <= 0 {
			continue
		}
		jobs := make([]string, 0, 1+len(crew.Jobs))
		if strings.TrimSpace(crew.Job) != "" {
			jobs = append(jobs, strings.TrimSpace(crew.Job))
		}
		for _, item := range crew.Jobs {
			if job := strings.TrimSpace(item.Job); job != "" {
				jobs = append(jobs, job)
			}
		}
		jobs = deduplicateNonEmpty(jobs)
		added := false
		for _, job := range jobs {
			personType := embyPersonTypeForJob(job)
			if personType == "" {
				continue
			}
			person := newTMDbPerson(crew.ID, name, personType, job, crew.ProfilePath)
			key := personIdentity(person)
			if idx, ok := seen[key]; ok {
				mergePersonRole(people[idx], job)
			} else if len(people) < tmdbPeopleMax {
				seen[key] = len(people)
				people = append(people, person)
			}
			added = true
		}
		if !added && len(people) >= tmdbPeopleMax {
			break
		}
	}
	return people, nil
}

func newTMDbPerson(personID int, name, personType, role, profilePath string) map[string]any {
	person := map[string]any{
		"Id":   tmdbPersonID(personID),
		"Name": name,
		"Type": personType,
		"Role": role,
	}
	if tag := tmdbProfileTag(profilePath); tag != "" {
		person["PrimaryImageTag"] = tag
	}
	return person
}

func personIdentity(person map[string]any) string {
	return fmt.Sprint(person["Id"]) + ":" + fmt.Sprint(person["Type"])
}

func mergePersonRole(person map[string]any, role string) {
	role = strings.TrimSpace(role)
	if role == "" {
		return
	}
	existing := strings.TrimSpace(fmt.Sprint(person["Role"]))
	if existing == "" {
		person["Role"] = role
		return
	}
	for _, part := range strings.Split(existing, " / ") {
		if strings.EqualFold(strings.TrimSpace(part), role) {
			return
		}
	}
	person["Role"] = existing + " / " + role
}

func deduplicateNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func embyPersonTypeForJob(job string) string {
	switch strings.ToLower(strings.TrimSpace(job)) {
	case "director":
		return "Director"
	case "writer", "screenplay", "story", "teleplay", "author":
		return "Writer"
	case "producer", "executive producer", "co-producer", "associate producer":
		return "Producer"
	case "composer", "original music composer", "music":
		return "Composer"
	default:
		return ""
	}
}

func tmdbPersonID(personID int) string {
	return tmdbPersonIDPrefix + strconv.Itoa(personID)
}

func parseTMDbPersonID(id string) (int, bool) {
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(id, tmdbPersonIDPrefix) {
		return 0, false
	}
	raw := strings.TrimPrefix(id, tmdbPersonIDPrefix)
	personID, err := strconv.Atoi(raw)
	return personID, err == nil && personID > 0
}

func tmdbProfileTag(profilePath string) string {
	profilePath = strings.TrimSpace(profilePath)
	if !tmdbProfilePathRE.MatchString(profilePath) {
		return ""
	}
	return tmdbProfileTagPrefix + profilePath + "?" + tmdbProfileTagVersion
}

func tmdbProfilePathFromTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if !strings.HasPrefix(tag, tmdbProfileTagPrefix) {
		return ""
	}
	profilePath := strings.TrimPrefix(tag, tmdbProfileTagPrefix)
	if idx := strings.IndexByte(profilePath, '?'); idx >= 0 {
		profilePath = profilePath[:idx]
	}
	if !tmdbProfilePathRE.MatchString(profilePath) {
		return ""
	}
	return profilePath
}

// ProfileImageURL builds the public TMDb image CDN URL for a profile path.
func (t *TMDbProvider) ProfileImageURL(profilePath string) string {
	if t == nil || !tmdbProfilePathRE.MatchString(strings.TrimSpace(profilePath)) {
		return ""
	}
	return strings.TrimRight(t.imgCDN, "/") + "/w300" + strings.TrimSpace(profilePath)
}

// PersonProfilePathByID resolves a person's current profile path directly
// from TMDb. Used when the client does not return the PrimaryImageTag.
func (t *TMDbProvider) PersonProfilePathByID(ctx context.Context, personID int) (string, error) {
	if t == nil || t.cfg == nil || personID <= 0 {
		return "", nil
	}
	apiKey := t.resolveAPIKey(ctx)
	if strings.TrimSpace(apiKey) == "" {
		return "", nil
	}
	q := url.Values{}
	q.Set("api_key", apiKey)
	q.Set("language", "zh-CN")
	endpoint := t.resolveBaseURL(ctx) + "/person/" + strconv.Itoa(personID) + "?" + q.Encode()
	var out struct {
		ProfilePath string `json:"profile_path"`
	}
	if err := t.getJSON(ctx, endpoint, &out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.ProfilePath), nil
}
