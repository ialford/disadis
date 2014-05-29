package disseminator

import (
	"io"
	"log"
	"net/http"
	"strings"
	"strconv"

	"github.com/dbrower/disadis/auth"
	"github.com/dbrower/disadis/fedora"
)

// Handles the route
//
//	GET	/:id
//
// And, if Versioned is true, the route
//
//	GET	/:id/:version
//
// The first route will return current version of the contents of the
// datastream named Ds.
// The second will either return the current version of the contents of
// Ds, provided the current version is equal to :version. Otherwise,
// a 403 Error is returned.
//
// If Auth is not nil, the object with the given identifier is passed
// to Auth, which may either return an error, a redirect, or nothing.
// If nothing is returned, the contents are passed back.
// The Auth handling is done after the identifier is decoded, but before
// the version check, if any.
//
// The reason the Handler calls Auth directly, instead of presuming
// the auth handler has wrapped this one, is because this handler knows
// how to parse the id out of the url, and it seems easier to just pass
// the id to the auth handler than to have the auth handler do the same
// thing.
//
// A pid namespace prefix can be assigned. It will be prepended to
// any decoded identifiers. Nothing is put between the prefix and the
// id, so include any colons in the prefix. e.g. "vecnet:"
//
// Note that because the identifier is pulled from the URL, identifiers
// containing forward slashes are problematic and are not handled.
//
// Example Usage:
//	fedora := "http://fedoraAdmin:fedoraAdmin@localhost:8983/fedora/"
//	ha := NewHydraAuth(fedora, "vecnet:")
//	ha.Handler = NewDownloadHandler(NewRemoteFedora(fedora, "vecnet:"))
//	http.Handle("/d/", http.StripPrefix("/d/", ha))
//	return http.ListenAndServe(":"+port, nil)
type DownloadHandler struct {
	Fedora fedora.Fedora
	Ds string
	Versioned bool
	Prefix string
	Auth *auth.HydraAuth
}

func NewDownloadHandler(f fedora.Fedora) http.Handler {
	return &DownloadHandler{
		Fedora: f,
	}
}

func notFound(w http.ResponseWriter) {
	http.Error(w, "404 Not Found", http.StatusNotFound)
}

func (dh *DownloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)

	if r.Method != "GET" {
		notFound(w)
		return
	}

	// "" / "id" ( / :version )?
	path := strings.TrimPrefix(r.URL.Path, "/")
	path = strings.TrimSuffix(path, "/")
	components := strings.SplitN(path, "/", 2)

	var (
		pid = dh.Prefix + components[0]	// sanitize pid somehow?
		version int = -1	// -1 == current version
		err error
	)
	// auth?
	if dh.Auth != nil {
		switch dh.Auth.Check(r, pid) {
		case auth.AuthDeny:
			// TODO: add WWW-Authenticate header field
			http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
			return
		case auth.AuthNotFound:
			notFound(w)
			return
		case auth.AuthAllow:
			break
		case auth.AuthError:
			fallthrough
		default:
			http.Error(w, "500 Server Error", http.StatusInternalServerError)
			return
		}
	}
	// figure out versions
	switch len(components) {
	case 1:
		// match /:id
		/* nothing to be done */
	case 2:
		// match /:id/:version
		if dh.Versioned {
			version, err = strconv.Atoi(components[1])
			if err == nil && version >= 0 {
				break
			}
		}
		fallthrough
	default:
		notFound(w)
		return
	}

	dsinfo, err := dh.Fedora.GetDatastreamInfo(pid, dh.Ds)
	if err != nil {
		log.Println(err)
		notFound(w)
		return
	}

	// does the version requested match the current version number?
	if version >= 0 && version != dh.currentVersion(dsinfo) {
		http.Error(w, "403 Forbidden", http.StatusForbidden)
		return
	}

	// e-tag match?
	etags, ok := r.Header["If-None-Match"]
	if ok {
		for i := range etags {
			if etags[i] == dsinfo.VersionID {
				w.Header().Set("ETag", dsinfo.VersionID)
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}

	// return content
	content, info, err := dh.Fedora.GetDatastream(pid, dh.Ds)
	if err != nil {
		switch err {
		case fedora.FedoraNotFound:
			notFound(w)
			return
		default:
			log.Printf("Got fedora error: %s", err)
			http.Error(w, "500 Internal Error", http.StatusInternalServerError)
			return
		}
	}
	defer content.Close()

	// sometimes fedora appends an extra extension. see FCREPO-497 in the
	// fedora commons JIRA.
	w.Header().Set("Content-Type", info.Type)
	w.Header().Set("Content-Length", info.Length)
	w.Header().Set("Content-Disposition", `inline; filename="` + dsinfo.Label + `"`)
	w.Header().Set("Content-Transfer-Encoding", "binary")
	w.Header().Set("Cache-Control", "private")
	w.Header().Set("ETag", dsinfo.VersionID)

	io.Copy(w, content)
	return
}

// returns -1 on error
func (dh *DownloadHandler) currentVersion(info fedora.DsInfo) int {
	// VersionID has the form "something.X"
	i := strings.LastIndex(info.VersionID, ".")
	if i == -1 {
		log.Println("Error parsing", info.VersionID)
		return -1
	}
	version, err := strconv.Atoi(info.VersionID[i+1:])
	if err != nil {
		log.Println(err)
		return -1
	}
	return version
}
