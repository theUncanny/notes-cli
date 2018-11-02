package notes

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	reTitleBar = regexp.MustCompile("^=+$")
)

type Note struct {
	Config   *Config
	Category string
	Tags     []string
	Created  time.Time
	File     string
	Title    string
}

func (note *Note) DirPath() string {
	return filepath.Join(note.Config.HomePath, note.Category)
}

func (note *Note) FilePath() string {
	return filepath.Join(note.Config.HomePath, note.Category, note.File)
}

func (note *Note) RelFilePath() string {
	return filepath.Join(note.Category, note.File)
}

func (note *Note) Create() error {
	var b bytes.Buffer

	// Write title
	title := note.Title
	if title == "" {
		title = strings.TrimSuffix(note.File, filepath.Ext(note.File))
	}
	b.WriteString(title + "\n")
	b.WriteString(strings.Repeat("=", len(title)) + "\n")

	// Write metadata
	fmt.Fprintf(&b, "- Category: %s\n", note.Category)
	fmt.Fprintf(&b, "- Tags: %s\n", strings.Join(note.Tags, ", "))
	fmt.Fprintf(&b, "- Created: %s\n\n", note.Created.Format(time.RFC3339))

	d := note.DirPath()
	if err := os.MkdirAll(d, 0755); err != nil {
		return errors.Wrapf(err, "Could not create category directory '%s'", d)
	}

	p := filepath.Join(d, note.File)
	if _, err := os.Stat(p); err == nil {
		return errors.Errorf("Cannot create new note since file '%s' already exists. Please edit it", note.RelFilePath())
	}

	return errors.Wrap(ioutil.WriteFile(p, b.Bytes(), 0644), "Cannot write note to file")
}

func (note *Note) Open() error {
	if note.Config.EditorPath == "" {
		return errors.New("Editor is not set. To open note in editor, please set $NOTES_CLI_EDITOR")
	}
	c := exec.Command(note.Config.EditorPath, note.FilePath())
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	c.Dir = note.DirPath()
	return errors.Wrap(c.Run(), "Editor command did not run successfully")
}

func (note *Note) ReadBodyN(maxBytes int64) (string, error) {
	path := note.FilePath()
	f, err := os.Open(path)
	if err != nil {
		return "", errors.Wrap(err, "Cannot open note file")
	}
	defer f.Close()

	// Skip metadata
	r := bufio.NewReader(f)
	sawCat, sawTags, sawCreated := false, false, false
	for {
		t, err := r.ReadString('\n')
		if strings.HasPrefix(t, "- Category: ") {
			sawCat = true
		} else if strings.HasPrefix(t, "- Tags:") {
			sawTags = true
		} else if strings.HasPrefix(t, "- Created: ") {
			sawCreated = true
		}
		if sawCat && sawTags && sawCreated {
			break
		}
		if err != nil {
			return "", errors.Wrapf(err, "Cannot read metadata of note file. Some metadata may be missing in '%s'", note.RelFilePath())
		}
	}

	var buf bytes.Buffer

	// Skip empty lines
	for {
		b, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		if len(b) > 1 {
			buf.Write(b)
			break
		}
	}

	len := int64(buf.Len())
	if len > maxBytes {
		return string(buf.Bytes()[:maxBytes]), nil
	}
	maxBytes -= len

	if _, err := io.CopyN(&buf, r, maxBytes); err != nil && err != io.EOF {
		return "", err
	}

	return buf.String(), nil
}

func NewNote(cat, tags, file, title string, cfg *Config) (*Note, error) {
	if cat == "" {
		return nil, errors.New("Category cannot be empty")
	}
	if file == "" {
		return nil, errors.New("File name cannot be empty")
	}
	ts := []string{}
	for _, t := range strings.Split(tags, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			ts = append(ts, t)
		}
	}

	file = strings.Replace(file, " ", "-", -1)
	if !strings.HasSuffix(file, ".md") {
		file += ".md"
	}
	return &Note{cfg, cat, ts, time.Now(), file, title}, nil
}

func LoadNote(path string, cfg *Config) (*Note, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "Cannot open note file")
	}
	defer f.Close()

	note := &Note{Config: cfg}

	note.File = filepath.Base(path)

	s := bufio.NewScanner(f)
	titleFound := false
	for s.Scan() {
		line := s.Text()
		// First line is title
		if !titleFound {
			if reTitleBar.MatchString(line) {
				if note.Title == "" {
					note.Title = "(no title)"
				}
				titleFound = true
			} else {
				note.Title = line
			}
		} else if strings.HasPrefix(line, "- Category: ") {
			note.Category = strings.TrimSpace(line[12:])
			if c := filepath.Base(filepath.Dir(path)); c != note.Category {
				return nil, errors.Errorf("Category does not match between file path and file content, in path '%s' v.s. in file '%s'", c, note.Category)
			}
		} else if strings.HasPrefix(line, "- Tags:") {
			tags := strings.Split(strings.TrimSpace(line[7:]), ",")
			note.Tags = make([]string, 0, len(tags))
			for _, t := range tags {
				t = strings.TrimSpace(t)
				if t != "" {
					note.Tags = append(note.Tags, t)
				}
			}
		} else if strings.HasPrefix(line, "- Created: ") {
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(line[11:]))
			if err != nil {
				return nil, errors.Wrapf(err, "Cannot parse created date time as RFC3339 format: %s", line)
			}
			note.Created = t
		}
		if note.Category != "" && note.Tags != nil && !note.Created.IsZero() && note.Title != "" {
			break
		}
	}
	if err := s.Err(); err != nil {
		return nil, errors.Wrapf(err, "Cannot read note file '%s'", canonPath(path))
	}

	if !titleFound {
		return nil, errors.Errorf("No title found in note '%s'. Didn't you use '====' bar for h1 title?", canonPath(path))
	}

	if note.Category == "" || note.Tags == nil || note.Created.IsZero() {
		return nil, errors.Errorf("Missing metadata in file '%s'. 'Category', 'Tags', 'Created' are mandatory", canonPath(path))
	}

	return note, nil
}

func WalkNotes(path string, cfg *Config, pred func(path string, note *Note) error) error {
	return errors.Wrap(
		filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() {
				if info.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}

			if info.IsDir() || !strings.HasSuffix(path, ".md") {
				// Skip
				return nil
			}

			n, err := LoadNote(path, cfg)
			if err != nil {
				return err
			}

			return pred(path, n)
		}),
		"Error while traversing notes. If you're finding notes of specific category, directory for it may not exist",
	)
}
