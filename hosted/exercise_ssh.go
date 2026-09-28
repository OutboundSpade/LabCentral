package hosted

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/privatefile"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed exercise_guest.py
var exerciseGuest string

type exercisePayload struct {
	Action   string `json:"action"`
	Site     string `json:"site"`
	Index    string `json:"index"`
	Key      string `json:"key"`
	Password string `json:"password"`
}

func exerciseBaseline(src lc.Source) (exercisePayload, error) {
	var p exercisePayload
	if src.Lab.Name != "branch-services" {
		return p, fmt.Errorf("Exercises require Example Branch.")
	}
	for _, f := range src.Files {
		switch f.Path {
		case "web/apache-site.conf":
			p.Site = string(f.Content)
		case "web/public/index.php":
			p.Index = string(f.Content)
		case "web.yml":
			var plays []struct {
				Tasks []struct {
					User *struct {
						Name     string
						Password string
					} `yaml:"ansible.builtin.user"`
				}
			}
			if err := yaml.Unmarshal(f.Content, &plays); err != nil {
				return p, err
			}
			for _, play := range plays {
				for _, task := range play.Tasks {
					if task.User != nil && task.User.Name == "svc_reports" {
						p.Password = task.User.Password
					}
				}
			}
		}
	}
	if p.Index == "" || !strings.Contains(p.Site, "<VirtualHost *:80>") || !strings.HasPrefix(p.Password, "$6$") {
		return p, fmt.Errorf("Pinned Example Branch revision lacks a supported exercise baseline.")
	}
	return p, nil
}
func (s *Service) exerciseKey(instance string) ([]byte, string, error) {
	dir := filepath.Join(s.Config.Root, "exercise-keys")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, digest(instance)+".pem")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, "", e
		}
		block, e := ssh.MarshalPrivateKey(key, "")
		if e != nil {
			return nil, "", e
		}
		b = pem.EncodeToMemory(block)
		if e = privatefile.Write(path, b); e != nil {
			return nil, "", e
		}
	} else if err != nil {
		return nil, "", err
	}
	key, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, "", err
	}
	return b, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey()))) + " branch-support", nil
}

type exerciseSSH interface {
	WithSSHMaterial(context.Context, string, string, func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error
}

func (s *Service) executeExercise(ctx context.Context, team Team, run ExerciseRun, src lc.Source) (string, error) {
	engine, ok := s.Engine.(exerciseSSH)
	if !ok {
		return "", fmt.Errorf("SSH exercise execution unavailable")
	}
	payload, err := exerciseBaseline(src)
	if err != nil {
		return "", err
	}
	report := ""
	err = engine.WithSSHMaterial(ctx, team.InstanceID, "web", func(inst lc.Instance, m lc.PlannedMachine, r lc.Resource, key []byte) error {
		if inst.Plan.Source.Digest != src.Digest {
			return fmt.Errorf("Running lab does not match the pinned exercise source")
		}
		private, pub, err := s.exerciseKey(team.InstanceID)
		if err != nil {
			return err
		}
		payload.Key = pub
		client, err := remote.Dial(ctx, r.Address, "lab", key, m.HostPublicKey)
		if err != nil {
			return fmt.Errorf("Web SSH connection failed: %w", err)
		}
		defer client.Close()
		guest := func(action string) ([]byte, error) {
			payload.Action = action
			b, e := json.Marshal(payload)
			if e != nil {
				return nil, e
			}
			out, e := remote.Run(ctx, client, "sudo -n timeout --kill-after=5s 90s flock -w 5 /run/lock/labcentral-exercise.lock python3 -c "+quote(exerciseGuest)+" "+quote(base64.StdEncoding.EncodeToString(b)))
			if e != nil {
				return nil, fmt.Errorf("%s verification failed on web; reset or retry recovery (%v)", action, e)
			}
			return out, nil
		}
		probe := func(port string, want bool) error {
			var machine lc.PlannedMachine
			var resource lc.Resource
			for _, v := range inst.Plan.Machines {
				if v.Name == "workstation" {
					machine = v
				}
			}
			for _, v := range inst.Resources {
				if v.Kind == "machine" && v.Name == "workstation" {
					resource = v
				}
			}
			if resource.Address == "" {
				return fmt.Errorf("Workstation is unavailable for the web check")
			}
			c, e := remote.Dial(ctx, resource.Address, "lab", key, machine.HostPublicKey)
			if e != nil {
				return fmt.Errorf("Workstation verification connection failed: %w", e)
			}
			defer c.Close()
			command := "curl --noproxy '*' --silent --fail --max-time 8 --output /dev/null http://web:" + port + "/"
			if !want {
				command = "if " + command + "; then exit 1; else code=$?; test \"$code\" = 7; fi"
			}
			if _, e = remote.Run(ctx, c, command); e != nil {
				return fmt.Errorf("Workstation could not verify the expected web behavior on port %s", port)
			}
			return nil
		}
		if run.Action == "check" {
			b, e := guest("check")
			if e != nil {
				return e
			}
			var checks map[string]bool
			if e = json.Unmarshal(b, &checks); e != nil {
				return e
			}
			if checks["web"] && checks["portRestored"] && checks["keyRemoved"] && checks["homepageRestored"] && checks["artifactRemoved"] && probe("80", true) == nil {
				report = "Recovered: website reachable from workstation; exercise key and file changes removed."
			} else {
				report = "Not fully recovered: check the web service, port, homepage, support-account key, and /tmp/branch-review.txt."
			}
			return nil
		}
		if _, err = guest("reset"); err != nil {
			return err
		}
		if err = probe("80", true); err != nil {
			return fmt.Errorf("Baseline check failed; no break applied: %w", err)
		}
		if run.Action == "reset" {
			report = "Reset complete: website restored on port 80; exercise access removed."
			return nil
		}
		run.Status = "applying"
		run.Updated = time.Now().UTC()
		if err = s.Store.Put(ctx, "exercise", run.ID, run); err != nil {
			return err
		}
		if _, err = guest(run.Action); err != nil {
			return err
		}
		switch run.Action {
		case "stop-web":
			err = probe("80", false)
		case "change-port":
			if err = probe("80", false); err == nil {
				err = probe("8080", true)
			}
		case "ssh-intrusion":
			intruder, e := remote.Dial(ctx, r.Address, "svc_reports", private, m.HostPublicKey)
			if e != nil {
				return fmt.Errorf("Controlled SSH login failed; reset before continuing: %w", e)
			}
			_, err = remote.Run(ctx, intruder, "printf '%s\\n' 'Branch support review completed.' > /tmp/branch-review.txt")
			intruder.Close()
			if err == nil {
				err = probe("80", true)
			}
		}
		if err != nil {
			return err
		}
		report = "Break active; baseline reset and expected symptoms verified."
		return nil
	})
	return report, err
}
