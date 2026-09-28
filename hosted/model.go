// Package hosted adds identity, team policy and a durable worker around LabCentral.
package hosted

import (
	"context"
	"database/sql"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"golang.org/x/oauth2"
	"time"
)

type Config struct {
	Root              string `json:"root"`
	Origin            string `json:"origin"`
	DesktopOrigin     string `json:"desktopOrigin"`
	Issuer            string `json:"issuer"`
	BackchannelOrigin string `json:"backchannelOrigin,omitempty"`
	ClientID          string `json:"clientID"`
	ClientSecret      string `json:"clientSecret"`
	Repository        string `json:"repository"`
	DeployKey         string `json:"deployKey"`
	KnownHosts        string `json:"knownHosts"`
	Environment       string `json:"environment"`
	WebRoot           string `json:"webRoot"`
	SDKRoot           string `json:"sdkRoot"`
}
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Admin bool   `json:"admin"`
}
type Session struct {
	ID      string        `json:"id"`
	User    User          `json:"user"`
	CSRF    string        `json:"csrf"`
	Token   *oauth2.Token `json:"token"`
	Expires time.Time     `json:"expires"`
	Checked time.Time     `json:"checked"`
}
type Meeting struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Template string    `json:"template"`
	Status   string    `json:"status"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
}
type Team struct {
	ID         string `json:"id"`
	MeetingID  string `json:"meetingID"`
	Name       string `json:"name"`
	Capacity   int    `json:"capacity"`
	Members    int    `json:"members"`
	InstanceID string `json:"-"`
	Status     string `json:"status"`
}
type Membership struct {
	UserID      string `json:"userID"`
	TeamID      string `json:"teamID"`
	PendingTeam string `json:"pendingTeam"`
	Switching   bool   `json:"switching"`
}
type Template struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Commit   string    `json:"commit"`
	Handover string    `json:"handover"`
	Built    bool      `json:"built"`
	Source   lc.Source `json:"-"`
}
type Job struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Target  string    `json:"target"`
	Status  string    `json:"status"`
	Error   string    `json:"error,omitempty"`
	Created time.Time `json:"created"`
}
type Machine struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Desktop bool   `json:"desktop"`
}
type Teammate struct {
	Name string `json:"name"`
}
type LabView struct {
	Teammates []Teammate `json:"teammates"`
	Team      *Team      `json:"team"`
	Machines  []Machine  `json:"machines"`
	Handover  string     `json:"handover"`
}
type Overview struct {
	User       User       `json:"user"`
	CSRF       string     `json:"csrf"`
	Meeting    *Meeting   `json:"meeting"`
	Membership Membership `json:"membership"`
	Teams      []Team     `json:"teams"`
	Lab        LabView    `json:"lab"`
}
type AdminView struct {
	Meeting   *Meeting   `json:"meeting"`
	Teams     []Team     `json:"teams"`
	Jobs      []Job      `json:"jobs"`
	Templates []Template `json:"templates"`
}
type CreateMeeting struct {
	Name      string `json:"name"`
	Template  string `json:"template"`
	Hours     int    `json:"hours"`
	TeamCount int    `json:"teamCount"`
	Capacity  int    `json:"capacity"`
}
type JoinTeam struct {
	TeamID string `json:"teamID"`
}
type ExtendMeeting struct {
	Hours int `json:"hours"`
}
type TargetRequest struct {
	ID string `json:"id"`
}
type Launch struct {
	URL    string `json:"url"`
	Ticket string `json:"ticket"`
}
type Identity interface {
	LoginURL(state, nonce, verifier string) string
	Exchange(context.Context, string, string, string) (User, *oauth2.Token, error)
	Refresh(context.Context, *oauth2.Token) (User, *oauth2.Token, error)
}
type Engine interface {
	Build(context.Context, lc.Plan) ([]lc.ImageRecord, error)
	DeployWithID(context.Context, lc.Plan, string) (lc.Instance, error)
	Inspect(context.Context, string) (lc.Instance, error)
	Recover(context.Context, string) (lc.Instance, error)
	RecoverBuild(context.Context, string) ([]lc.ImageRecord, error)
	Destroy(context.Context, string) error
	OpenDesktopWithOptions(context.Context, string, string, lc.AccessOptions) (lc.AccessInfo, error)
	CloseAccess(context.Context, string) error
}

// SQLite uses a single writer connection; engine state has its own WAL database.
type database interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}
