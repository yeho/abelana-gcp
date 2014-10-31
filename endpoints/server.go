// Copyright 2014 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package abelana

import (
	//    "fmt"

	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"time"

	"appengine"
	"appengine/datastore"
	"appengine/delay"
	"appengine/urlfetch"
	"appengine/user"

	auth "code.google.com/p/google-api-go-client/oauth2/v2"

	"github.com/go-martini/martini"
)

////////////////////////////////////////////////////////////////////
const EnableBackdoor = true // FIXME(lesv) TEMPORARY BACKDOOR ACCESS
const enableStubs = true

////////////////////////////////////////////////////////////////////

// These things shouldn't be here, but there isn't a good place to get them at the moment.
const (
	authEmail         = "416523807683-87kpuu2fsvov4hbg9j8f8808an8h2k2b@developer.gserviceaccount.com"
	projectID         = "abelana-222"
	bucket            = "abelana-in"
	redisExt          = "146.148.88.48:6379"
	uploadRetries     = 5
	timelineBatchSize = 100
)

var aconfig *AbelanaConfig

// In redis we store the following:
// IM:uuuuuu.ppppppp HASH an imageID
//   date  is the date the photo was added
//   flag  DON'T SHOW THIS TO OTHERS 'TIL REVIEW -- must get +2
//   uuuuuu is the id of a user that likes the photo
//   (Total count of likes is (HLEN k) -1)
//
// TL:uuuuuu LIST The timeline[max 2000] for each user. (list of photos)
// HT:uuuuuu HASH
//   dn is the displayName for the user.

// In datastore we have the following:
// User >> Photo >> Like
//               >> Comments

var delayFunc = delay.Func("test003", func(cx appengine.Context, x string) {
	cx.Infof("delay happened " + x)
})

var delayCopyImage = delay.Func("CopyImage001", copyUserPhoto)
var delayAddPhoto = delay.Func("AddImage002", addPhoto)
var delayINowFollow = delay.Func("FollowByID03", iNowFollow)
var delayFindFollows = delay.Func("findFollows334", findFollows)

// AbelanaConfig contains all the information we need to run Abelana
type AbelanaConfig struct {
	authEmail         string
	projectID         string
	bucket            string
	redisPW           string
	redis             string
	timelineBatchSize int
	uploadRetries     int
	EnableBackdoor    bool
	enableStubs       bool
}

// User is the root structure for everything.
type User struct {
	UserID        string
	DisplayName   string
	Email         string
	FollowsMe     []string
	IFollow       []string
	IWantToFollow []string
}

// Photo is how we keep images in Datastore
type Photo struct {
	PhotoID string
	Date    int64
}

// ToLike knows about who likes you.
type ToLike struct {
	UserID string
}

// ATOKJson is the json message for an Access Token (TEMPORARY - Until GitKit supports this)
type ATOKJson struct {
	Kind string `json:"kind"`
	Atok string `json:"atok"`
}

// Status is what we return if we have nothing to return
type Status struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// TLEntry holds timeline entries
type TLEntry struct {
	Created int64  `json:"created"`
	UserID  string `json:"userid"`
	Name    string `json:"name"`
	PhotoID string `json:"photoid"`
	Likes   int    `json:"likes"`
	ILike   bool   `json:"ilike"`
}

// Timeline the data the client sees.
type Timeline struct {
	Kind    string    `json:"kind"`
	Entries []TLEntry `json:"entries"`
}

// Person holds information about our followers
type Person struct {
	kind     string `json:"kind,omitempty"`
	PersonID string `json:"personid"`
	Email    string `json:"email,omitempty"`
	Name     string `json:"name"`
}

// Persons holds a list of our followers
type Persons struct {
	kind    string   `json:"kind"`
	Persons []Person `json:"persons"`
}

// Comment holds all comments
type Comment struct {
	PersonID string `json:"personid"`
	Text     string `json:"text"`
	Time     int64  `json:"time"`
}

// Comments returned from GetComments()
type Comments struct {
	Kind    string    `json:"kind"`
	Entries []Comment `json:"entries"`
}

// Stats contains usefull user statistics
type Stats struct {
	Following int `json:"following"`
	Followers int `json:"followers"`
}

// AppEngine middleware inserts a context where it's needed.
func AppEngine(c martini.Context, r *http.Request) {
	c.MapTo(appengine.NewContext(r), (*appengine.Context)(nil))
}

func init() {
	aconfig, _ = loadAbelanaConfig("private/abelana-config.json")

	m := martini.Classic()
	m.Use(AppEngine)

	m.Get("/user/:gittok/login/:displayName/:photoUrl", Login)                  // => ATOKJson
	m.Get("/user/:atok/refresh", Aauth, Refresh)                                // => ATOKJson
	m.Get("/user/:atok/useful", Aauth, GetSecretKey)                            // => Status
	m.Delete("/user/:atok", Aauth, Wipeout)                                     // => Status
	m.Post("/user/:atok/following/facebook/:fbkey", Aauth, Import)              // => Status
	m.Post("/user/:atok/following/plus/:plkey", Aauth, Import)                  // => Status
	m.Post("/user/:atok/following/yahoo/:ykey", Aauth, Import)                  // => Status
	m.Get("/user/:atok/following", Aauth, GetFollowing)                         // => Persons
	m.Put("/user/:atok/following/:personid", Aauth, FollowByID)                 // => Status
	m.Get("/user/:atok/following/:personid", Aauth, GetPerson)                  // => Person
	m.Put("/user/:atok/follow/:email", Aauth, Follow)                           // => Status
	m.Put("/user/:atok/device/:regid", Aauth, Register)                         // => Status
	m.Get("/user/:atok/stats", Aauth, Statistics)                               // => Stats
	m.Delete("/user/:atok/device/:regid", Aauth, Unregister)                    // => Status
	m.Get("/user/:atok/timeline/:lastid", Aauth, GetTimeLine)                   // => Timeline
	m.Get("/user/:atok/profile/:lastdate", Aauth, GetMyProfile)                 // => Timeline
	m.Get("/user/:atok/following/:personid/profile/:lastdate", Aauth, FProfile) // => Timeline
	m.Post("/photo/:atok/:photoid/comment/:text", Aauth, SetPhotoComments)      // => Status
	m.Get("/photo/:atok/:photoid/comments", Aauth, GetPhotoComments)            // => Comments
	m.Put("/photo/:atok/:photoid/like", Aauth, Like)                            // => Status
	m.Delete("/photo/:atok/:photoid/like", Aauth, Unlike)                       // => Status
	m.Get("/photo/:atok/:photoid/flag", Aauth, Flag)                            // => Status

	m.Post("/photopush/:superid", PostPhoto) // "ok"

	if EnableBackdoor {
		m.Get("/les", Test)
		m.Get("/user/:gittok/login", Login)
	}

	tokenInit()

	http.Handle("/", m)
	redisInit()
}

// loadAbelanaConfig loads the configuration from the config file specified by path.
func loadAbelanaConfig(path string) (*AbelanaConfig, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c AbelanaConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Test does magic of the moment
func Test(cx appengine.Context) string {
	cx.Infof("Test...")
	delayFunc.Call(cx, "hello world")
	return `ok`
}

// replyJSON Given an object, convert to JSON and reply with it
func replyJSON(w http.ResponseWriter, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Add("Content-Type", "application/json")
	_, err = w.Write(b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func replyOk(w http.ResponseWriter) {
	st := &Status{"abelana#status", "ok"}
	replyJSON(w, st)
}

///////////////////////////////////////////////////////////////////////////////////////////////////
// Timeline
///////////////////////////////////////////////////////////////////////////////////////////////////

// GetTimeLine - get the timeline for the user (token) : TlResp
func GetTimeLine(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	var timeline []TLEntry
	var err error

	if !enableStubs {
		timeline, err = getTimeline(cx, at.ID(), p["lastid"])
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		t := time.Now().Unix()
		if p["lastid"] != "0" {
			timeline = []TLEntry{
				TLEntry{t - 200, "00001", "Les", "0001", 1, false},
				TLEntry{t - 1000, "00001", "Les", "0002", 99, false},
				TLEntry{t - 2500, "00001", "Les", "0003", 0, false},
				TLEntry{t - 6040, "00001", "Les", "0004", 3, true},
				TLEntry{t - 7500, "00001", "Les", "0005", 1, true},
				TLEntry{t - 9300, "00001", "Les", "0006", 99, false},
				TLEntry{t - 10200, "00001", "Les", "0007", 0, false},
				TLEntry{t - 47003, "00001", "Les", "0008", 3, false},
				TLEntry{t - 53002, "00001", "Les", "0009", 1, true},
				TLEntry{t - 54323, "00001", "Les", "0010", 99, false},
				TLEntry{t - 56112, "00001", "Les", "0011", 0, false},
				TLEntry{t - 58243, "00001", "Les", "0004", 3, false}}
		} else {
			timeline = []TLEntry{
				TLEntry{t - 95323, "00002", "Zafir", "0002", 99, false},
				TLEntry{t - 96734, "00002", "Zafir", "0003", 0, false},
				TLEntry{t - 98033, "00002", "Zafir", "0004", 3, false},
				TLEntry{t - 99334, "00002", "Zafir", "0005", 1, false},
				TLEntry{t - 99993, "00002", "Zafir", "0006", 99, false},
				TLEntry{t - 102304, "00002", "Zafir", "0007", 0, false},
				TLEntry{t - 102750, "00002", "Zafir", "0008", 3, false},
				TLEntry{t - 104333, "00002", "Zafir", "0009", 1, false}}
		}
	}
	tl := &Timeline{"abelana#timeline", timeline}
	replyJSON(w, tl)
}

// GetMyProfile - Get my entries only (token) : TlResp
func GetMyProfile(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	var err error
	var timeline []TLEntry

	if !enableStubs {
		timeline, err = profileForUser(cx, at.ID(), p["lastdate"])
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		t := time.Now().Unix()
		timeline = []TLEntry{
			TLEntry{t - 99993, "00001", "Les", "0006", 99, true},
			TLEntry{t - 102304, "00001", "Les", "0007", 0, false},
			TLEntry{t - 102750, "00001", "Les", "0008", 3, false},
			TLEntry{t - 104333, "00001", "Les", "0009", 1, false},
			TLEntry{t - 105323, "00001", "Les", "0010", 9, false},
			TLEntry{t - 107323, "00001", "Les", "0011", 0, false},
			TLEntry{t - 173404, "00001", "Les", "0005", 21, false}}
	}
	tl := &Timeline{"abelana#timeline", timeline}
	replyJSON(w, tl)
}

// FProfile - Get a specific followers entries only (TlfReq) : TlResp
func FProfile(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	var err error
	var timeline []TLEntry

	if !enableStubs {
		personID := p["personid"]
		timeline, err = profileForUser(cx, personID, p["lastdate"])
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

	} else {
		t := time.Now().Unix()
		timeline = []TLEntry{
			TLEntry{t - 80500, "00001", "Les", "0002", 99, true},
			TLEntry{t - 81200, "00001", "Les", "0003", 0, false},
			TLEntry{t - 89302, "00001", "Les", "0005", 3, true},
			TLEntry{t - 91200, "00001", "Les", "0007", 1, false},
			TLEntry{t - 92343, "00001", "Les", "0006", 99, true},
			TLEntry{t - 99334, "00001", "Les", "0005", 1, false},
			TLEntry{t - 99993, "00001", "Les", "0006", 99, false}}
	}
	tl := &Timeline{"abelana#timeline", timeline}
	replyJSON(w, tl)
}

// profileForUser will get the 300 most recent photos from the user, we don't provide any info
// on likes as that would require many trips to the datastore making the call really slow.
func profileForUser(cx appengine.Context, userID, lastdate string) ([]TLEntry, error) {
	timeline := []TLEntry{}

	k1 := datastore.NewKey(cx, "User", userID, 0, nil)
	user := &User{}
	err := datastore.Get(cx, k1, &user)
	if err != nil {
		cx.Errorf("profileForUser Get1 %v %v", userID, err)
	}
	q := datastore.NewQuery("Photo").Ancestor(k1)
	if lastdate != "" && lastdate != "0" {
		lastDate, err := strconv.ParseInt(lastdate, 10, 64)
		if err != nil {
			cx.Errorf("profileForUser ParseInt %v %v %v", userID, lastDate, err)
		}
		q = q.Filter("", lastDate)
	}
	q = q.Order("-Date").Limit(timelineBatchSize * 3)
	var photos []Photo
	_, err = q.GetAll(cx, &photos)
	if err != nil {
		cx.Errorf("profileForUser get2 %v %v", userID, err)
	}
	timeline = make([]TLEntry, 0, timelineBatchSize*3)

	for _, p := range photos {
		te := TLEntry{p.Date, userID, user.DisplayName, p.PhotoID, -1, false} // don't return anything for likes
		timeline = append(timeline, te)
	}
	return timeline, nil
}

///////////////////////////////////////////////////////////////////////////////////////////////////
// Import
///////////////////////////////////////////////////////////////////////////////////////////////////

// Import for Facebook / G+ / ... (xcred) : StatusResp
func Import(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	replyOk(w)
}

///////////////////////////////////////////////////////////////////////////////////////////////////
// Person
///////////////////////////////////////////////////////////////////////////////////////////////////

// GetFollowing - A list of those I follow (AToken) :
func GetFollowing(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	fl := &Persons{}

	if !enableStubs {
		k1 := datastore.NewKey(cx, "User", at.ID(), 0, nil)
		user := &User{}
		err := datastore.Get(cx, k1, &user)
		if err != nil {
			cx.Errorf("GetFollowing %v %v", at.ID(), err)
			replyOk(w)
		}
		pl := getPersons(cx, user.IFollow)
		fl = &Persons{"abelana#followerList", pl}
	} else {
		fl = &Persons{"abelana#followerList",
			[]Person{
				Person{"abelana#follower", "00001", "", "Les"},
				Person{"abelana#follower", "12730648828453578083", "", "Zafir"}}}
	}
	replyJSON(w, fl)
}

// GetPerson -- find out about someone  : Person
func GetPerson(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	f := &Person{}

	if !enableStubs {
		k1 := datastore.NewKey(cx, "User", p["personid"], 0, nil)
		user := &User{}
		err := datastore.Get(cx, k1, &user)
		if err != nil {
			cx.Errorf("GetPerson %v %v", p["personid"], err)
			replyOk(w)
		}
		f = &Person{"abelana#follower", user.UserID, user.Email, user.DisplayName}
	} else {
		f = &Person{"abelana#follower", "00001", "lesv@abelana-app.com", "Les Vogel"}
	}
	replyJSON(w, f)
}

// FollowByID - will tell us about a new possible follower (FrReq) : Status
func FollowByID(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	iFollow := p["personid"]
	err := followById(cx, at.ID(), iFollow)
	if err != nil {
		cx.Errorf("FollowByID: %v", err)
	}
	replyOk(w)
}

// Follow will see if we can follow the user, given their email
func Follow(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	var users []User
	var keys []datastore.Key
	eMail, err := decodeSegment(p["email"])
	if err != nil {
		cx.Errorf("Follow: ds %v %v", p["email"], err)
		replyOk(w)
		return
	}
	email := string(eMail)
	// TODO try looking them up in GitKit as it has many versions of email addresses.

	q := datastore.NewQuery("User").Filter("Email =", email).KeysOnly()
	err = datastore.RunInTransaction(cx, func(cx appengine.Context) error {
		keys, err := q.GetAll(cx, &users)
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			return nil // We found a user
		}
		user, err := findUser(cx, at.ID())
		if err != nil {
			return err
		}
		iwant := len(user.IWantToFollow)
		if iwant == cap(user.IWantToFollow) {
			newSlice := make([]string, iwant, iwant+1)
			copy(newSlice, user.IWantToFollow)
			user.IWantToFollow = newSlice[0 : iwant+1]
		}
		kUser := datastore.NewKey(cx, "User", at.ID(), 0, nil)
		user.IWantToFollow[iwant] = email
		_, err = datastore.Put(cx, kUser, &user)
		return err
	}, nil)
	if err != nil {
		cx.Errorf("Follow: %v %v", eMail, err)
		replyOk(w)
		return
	}
	if len(keys) > 0 {
		followById(cx, at.ID(), keys[0].StringID())
	}
	replyOk(w)
}

// findFollows will do the major explosion for the social network, it is called by Delay and it will
// fire off many delay's possibly for a popular person joining the network.
func findFollows(cx appengine.Context, userID, email string) {

}

// followById makes following a user easy once we know who they are
func followById(cx appengine.Context, userID, followingID string) error {

	err := datastore.RunInTransaction(cx, func(cx appengine.Context) error {
		user := &User{}
		kUser := datastore.NewKey(cx, "User", userID, 0, nil)
		err := datastore.Get(cx, kUser, &user)
		if err != nil {
			return fmt.Errorf("getMe %v %v %v", userID, followingID, err)
		}

		followed := &User{}
		kFollowed := datastore.NewKey(cx, "User", followingID, 0, nil)
		err = datastore.Get(cx, kFollowed, &followed)
		if err != nil {
			return fmt.Errorf("getFollowed %v %v %v", followingID, userID, err)
		}

		sl := user.IFollow
		if len(sl) == cap(sl) {
			newSl := make([]string, len(sl), len(sl)+1)
			copy(newSl, sl)
			sl = newSl
		}
		user.IFollow = sl[0 : len(sl)+1]
		user.IFollow[len(sl)] = followingID

		sl = user.FollowsMe
		if len(sl) == cap(sl) {
			newSl := make([]string, len(sl), len(sl)+1)
			copy(newSl, sl)
			sl = newSl
		}
		user.FollowsMe = sl[0 : len(sl)+1]
		user.FollowsMe[len(sl)] = userID

		_, err = datastore.Put(cx, kUser, &user)
		if err != nil {
			return fmt.Errorf("updateMe %v %v", userID, err)
		}
		_, err = datastore.Put(cx, kFollowed, &followed)
		if err != nil {
			return fmt.Errorf("updateFollowed %v %v", followingID, err)
		}
		return nil
	}, nil)

	if err != nil {
		return err
	}
	delayINowFollow.Call(cx, userID, followingID)
	return nil
}

// Statistics will tell you about a user
func Statistics(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	st := &Stats{300, 30}
	replyJSON(w, st)
}

///////////////////////////////////////////////////////////////////////////////////////////////////
// Photo
///////////////////////////////////////////////////////////////////////////////////////////////////

// SetPhotoComments allows the users voice to be heard (PhotoComment) : Status
func SetPhotoComments(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	s := strings.Split(p["photoid"], ".")
	if len(s) != 2 {
		replyOk(w)
		return
	}
	userID, photoID := s[0], p["photoid"]

	tod := time.Now().UTC().Unix()
	k1 := datastore.NewKey(cx, "User", userID, 0, nil)
	k2 := datastore.NewKey(cx, "Photo", photoID, 0, k1)
	k3 := datastore.NewKey(cx, "Comment", "", tod, k2)
	c := &Comment{at.ID(), p["text"], tod}
	_, err := datastore.Put(cx, k3, c)
	if err != nil {
		cx.Errorf("SetPhotoComments: %v %v", k3, err)
	}
	replyOk(w)
}

// GetPhotoComments will get the comments given a photoid
func GetPhotoComments(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	var c []Comment

	s := strings.Split(p["photoid"], ".")
	if len(s) != 2 {
		replyOk(w)
		return
	}
	userID, photoID := s[0], p["photoid"]
	k1 := datastore.NewKey(cx, "User", userID, 0, nil)
	k2 := datastore.NewKey(cx, "Photo", photoID, 0, k1)

	q := datastore.NewQuery("Comment").Ancestor(k2).Order("Time")
	_, err := q.GetAll(cx, &c)
	if err != nil {
		cx.Errorf("GetPhotoComments %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cl := &Comments{"abelana#comments", c}
	replyJSON(w, cl)
}

// Like let's the user tell of their joy (Photo) : Status
func Like(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	s := strings.Split(p["photoid"], ".")
	if len(s) != 2 {
		replyOk(w)
		return
	}
	userID, photoID := s[0], p["photoid"]

	like(cx, at.ID(), photoID)

	k1 := datastore.NewKey(cx, "User", userID, 0, nil)
	k2 := datastore.NewKey(cx, "Photo", photoID, 0, k1)
	k3 := datastore.NewKey(cx, "Like", at.ID(), 0, k2)
	l := &ToLike{at.ID()}
	_, err := datastore.Put(cx, k3, l)
	if err != nil {
		cx.Errorf("Like: %v %v", k3, err)
	}
	replyOk(w)
}

// Unlike let's the user recind their +1 (Photo) : Status
func Unlike(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	s := strings.Split(p["photoid"], ".")
	if len(s) != 2 {
		replyOk(w)
		return
	}
	userID, photoID := s[0], p["photoid"]
	k1 := datastore.NewKey(cx, "User", userID, 0, nil)
	k2 := datastore.NewKey(cx, "Photo", photoID, 0, k1)
	k3 := datastore.NewKey(cx, "Like", at.ID(), 0, k2)
	err := datastore.Delete(cx, k3)
	if err != nil {
		cx.Errorf("Unlike: %v %v", k3, err)
		replyOk(w)
		return
	}
	unlike(cx, at.ID(), photoID)
	replyOk(w)
}

// Flag will bring this to the administrators attention.
func Flag(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	s := strings.Split(p["photoid"], ".")
	if len(s) != 2 {
		replyOk(w)
		return
	}
	flag(cx, at.ID(), p["photoid"])

	//  We should also write something to Datastore

	replyOk(w)
}

// PostPhoto lets us know that we have a photo, we then tell both DataStore and Redis
func PostPhoto(cx appengine.Context, p martini.Params, w http.ResponseWriter, rq *http.Request) string {
	cx.Infof("PostPhoto %v", p["superid"])
	otok := rq.Header.Get("Authorization")
	if !appengine.IsDevAppServer() {
		ok, err := authorized(cx, otok)
		if !ok || err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return ``
		}
	}
	s := strings.Split(p["superid"], ".")
	if len(s) == 3 { // We only need to call for userid.photoID.webp
		delayAddPhoto.Call(cx, p["superid"])
	}
	return `ok`
}

// authorized verifies the auth token.  We could do this ourselves using Admin if our caller had used
// the right service account, but this will do it for any account.
func authorized(cx appengine.Context, token string) (bool, error) {
	if user.IsAdmin(cx) {
		cx.Infof("authorized - true")
		return true, nil
	}

	if fs := strings.Fields(token); len(fs) == 2 && fs[0] == "Bearer" {
		token = fs[1]
	} else {
		return false, nil
	}

	svc, err := auth.New(urlfetch.Client(cx))
	if err != nil {
		return false, err
	}
	tok, err := svc.Tokeninfo().Access_token(token).Do()
	if err != nil {
		return false, err
	}
	cx.Infof("  tok %v", tok)
	return tok.Email == authEmail, nil
}

///////////////////////////////////////////////////////////////////////////////////////////////////
// Management
///////////////////////////////////////////////////////////////////////////////////////////////////

// Wipeout will erase all data you are working on. (Atok) : Status
func Wipeout(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {

	replyOk(w)
}

// Register will start GCM messages to your device (GCMReq) : Status
func Register(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	replyOk(w)
}

// Unregister will stop GCM messages from going to your device (GCMReq) : Status
func Unregister(cx appengine.Context, at Access, p martini.Params, w http.ResponseWriter) {
	replyOk(w)
}
