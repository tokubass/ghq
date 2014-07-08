package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"

	"code.google.com/p/goauth2/oauth"
	"github.com/codegangsta/cli"
	"github.com/google/go-github/github"
	"github.com/motemen/ghq/pocket"
	"github.com/motemen/ghq/utils"
)

var Commands = []cli.Command{
	commandGet,
	commandList,
	commandWhich,
	commandLook,
	commandImport,
}

var commandGet = cli.Command{
	Name:  "get",
	Usage: "Clone/sync with a remote repository",
	Description: `
    Clone a GitHub repository under ghq root direcotry. If the repository is
    already cloned to local, nothing will happen unless '-u' ('--update')
    flag is supplied, in which case 'git remote update' is executed.
    When you use '-p' option, the repository is cloned via SSH.
`,
	Action: doGet,
	Flags: []cli.Flag{
		cli.BoolFlag{"update, u", "Update local repository if cloned already"},
		cli.BoolFlag{"p", "Clone with SSH"},
		cli.BoolFlag{"shallow", "Do a shallow clone"},
	},
}

var commandList = cli.Command{
	Name:  "list",
	Usage: "List local repositories",
	Description: `
    List locally cloned repositories. If a query argument is given, only
    repositories whose names contain that query text are listed. '-e'
    ('--exact') forces the match to be an exact one (i.e. the query equals to
    _project_ or _user_/_project_) If '-p' ('--full-path') is given, the full paths
    to the repository root are printed instead of relative ones.
`,
	Action: doList,
	Flags: []cli.Flag{
		cli.BoolFlag{"exact, e", "Perform an exact match"},
		cli.BoolFlag{"full-path, p", "Print full paths"},
		cli.BoolFlag{"unique", "Print unique subpaths"},
	},
}

var commandWhich = cli.Command{
	Name:        "which",
	Usage:       "Show full path of local repository",
	Description: `which`,
	Action:      doWhich,
}

var commandLook = cli.Command{
	Name:  "look",
	Usage: "Look into a local repository",
	Description: `
    Look into a locally cloned repository with the shell.
`,
	Action: doLook,
}

var commandImport = cli.Command{
	Name:  "import",
	Usage: "Import repositories from other web services",
	Subcommands: []cli.Command{
		commandImportStarred,
		commandImportPocket,
	},
}

var commandImportStarred = cli.Command{
	Name:  "starred",
	Usage: "Get all starred GitHub repositories",
	Description: `
    Retrieves GitHub repositories that are starred by the user specified and
    performs 'get' for each of them.
`,
	Action: doImportStarred,
	Flags: []cli.Flag{
		cli.BoolFlag{"update, u", "Update local repository if cloned already"},
		cli.BoolFlag{"p", "Clone with SSH"},
		cli.BoolFlag{"shallow", "Do a shallow clone"},
	},
}

var commandImportPocket = cli.Command{
	Name:  "pocket",
	Usage: "Get all github.com entries in Pocket",
	Description: `
    Retrieves Pocket <http://getpocket.com/> entries of github.com and
    performs 'get' for each of them.
`,
	Action: doImportPocket,
	Flags: []cli.Flag{
		cli.BoolFlag{"update, u", "Update local repository if cloned already"},
	},
}

type commandDoc struct {
	Parent    string
	Arguments string
}

var commandDocs = map[string]commandDoc{
	"get":     {"", "[-u] <repository URL> | [-u] [-p] <user>/<project>"},
	"list":    {"", "[-p] [-e] [<query>]"},
	"which":   {"", "<project> | <user>/<project> | <host>/<user>/<project>"},
	"look":    {"", "<project> | <user>/<project> | <host>/<user>/<project>"},
	"import":  {"", "[-u] [-p] starred <user> | [-u] pocket"},
	"starred": {"import", "[-u] [-p] <user>"},
	"pocket":  {"import", "[-u]"},
}

// Makes template conditionals to generate per-command documents.
func mkCommandsTemplate(genTemplate func(commandDoc) string) string {
	template := "{{if false}}"
	for _, command := range append(Commands, commandImportStarred, commandImportPocket) {
		template = template + fmt.Sprintf("{{else if (eq .Name %q)}}%s", command.Name, genTemplate(commandDocs[command.Name]))
	}
	return template + "{{end}}"
}

func init() {
	argsTemplate := mkCommandsTemplate(func(doc commandDoc) string { return doc.Arguments })
	parentTemplate := mkCommandsTemplate(func(doc commandDoc) string { return string(strings.TrimLeft(doc.Parent+" ", " ")) })

	cli.CommandHelpTemplate = `NAME:
    {{.Name}} - {{.Usage}}

USAGE:
    ghq ` + parentTemplate + `{{.Name}} ` + argsTemplate + `
{{if (len .Description)}}
DESCRIPTION: {{.Description}}
{{end}}{{if (len .Flags)}}
OPTIONS:
    {{range .Flags}}{{.}}
    {{end}}
{{end}}`
}

func doGet(c *cli.Context) {
	argURL := c.Args().Get(0)
	doUpdate := c.Bool("update")
	isShallow := c.Bool("shallow")

	if argURL == "" {
		cli.ShowCommandHelp(c, "get")
		os.Exit(1)
	}

	url, err := NewURL(argURL)
	utils.DieIf(err)

	isSSH := c.Bool("p")
	if isSSH {
		// Assume Git repository if `-p` is given.
		url, err = ConvertGitURLHTTPToSSH(url)
		utils.DieIf(err)
	}

	remote, err := NewRemoteRepository(url)
	utils.DieIf(err)

	if remote.IsValid() == false {
		utils.Log("error", fmt.Sprintf("Not a valid repository: %s", url))
		os.Exit(1)
	}

	getRemoteRepository(remote, doUpdate, isShallow)
}

// getRemoteRepository clones or updates a remote repository remote.
// If doUpdate is true, updates the locally cloned repository. Otherwise does nothing.
// If isShallow is true, does shallow cloning. (no effect if already cloned or the VCS is Mercurial)
func getRemoteRepository(remote RemoteRepository, doUpdate bool, isShallow bool) {
	remoteURL := remote.URL()
	local := LocalRepositoryFromURL(remoteURL)

	path := local.FullPath
	newPath := false

	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			newPath = true
			err = nil
		}
		utils.PanicIf(err)
	}

	if newPath {
		utils.Log("clone", fmt.Sprintf("%s -> %s", remoteURL, path))

		vcs := remote.VCS()
		if vcs == nil {
			utils.Log("error", fmt.Sprintf("Could not find version control system: %s", remoteURL))
			os.Exit(1)
		}

		vcs.Clone(remoteURL, path, isShallow)
	} else {
		if doUpdate {
			utils.Log("update", path)
			local.VCS().Update(path)
		} else {
			utils.Log("exists", path)
		}
	}
}

func doList(c *cli.Context) {
	query := c.Args().First()
	exact := c.Bool("exact")
	printFullPaths := c.Bool("full-path")
	printUniquePaths := c.Bool("unique")

	var filterFn func(*LocalRepository) bool
	if query == "" {
		filterFn = func(_ *LocalRepository) bool {
			return true
		}
	} else if exact {
		filterFn = func(repo *LocalRepository) bool {
			return repo.Matches(query)
		}
	} else {
		filterFn = func(repo *LocalRepository) bool {
			return strings.Contains(repo.NonHostPath(), query)
		}
	}

	repos := []*LocalRepository{}

	walkLocalRepositories(func(repo *LocalRepository) {
		if filterFn(repo) == false {
			return
		}

		repos = append(repos, repo)
	})

	if printUniquePaths {
		subpathCount := map[string]int{} // Count duplicated subpaths (ex. foo/dotfiles and bar/dotfiles)
		reposCount := map[string]int{}   // Check duplicated repositories among roots

		// Primary first
		for _, repo := range repos {
			if reposCount[repo.RelPath] == 0 {
				for _, p := range repo.Subpaths() {
					subpathCount[p] = subpathCount[p] + 1
				}
			}

			reposCount[repo.RelPath] = reposCount[repo.RelPath] + 1
		}

		for _, repo := range repos {
			if reposCount[repo.RelPath] > 1 && repo.IsUnderPrimaryRoot() == false {
				continue
			}

			for _, p := range repo.Subpaths() {
				if subpathCount[p] == 1 {
					fmt.Println(p)
					break
				}
			}
		}
	} else {
		for _, repo := range repos {
			if printFullPaths {
				fmt.Println(repo.FullPath)
			} else {
				fmt.Println(repo.RelPath)
			}
		}
	}
}

func doWhich(c *cli.Context) {
	name := c.Args().First()

	if name == "" {
		cli.ShowCommandHelp(c, "which")
		os.Exit(1)
	}

	reposFound := []*LocalRepository{}
	walkLocalRepositories(func(repo *LocalRepository) {
		if repo.Matches(name) {
			reposFound = append(reposFound, repo)
		}
	})

	switch len(reposFound) {
	case 0:
		utils.Log("error", "No repository found")
	case 1:
		fmt.Println(reposFound[0].FullPath)
	default:
		utils.Log("error", "More than one repositories are found; Try more precise name")
		for _, repo := range reposFound {
			utils.Log("error", "- "+strings.Join(repo.PathParts, "/"))
		}
	}
}

func doLook(c *cli.Context) {
	name := c.Args().First()

	if name == "" {
		cli.ShowCommandHelp(c, "look")
		os.Exit(1)
	}

	reposFound := []*LocalRepository{}
	walkLocalRepositories(func(repo *LocalRepository) {
		if repo.Matches(name) {
			reposFound = append(reposFound, repo)
		}
	})

	switch len(reposFound) {
	case 0:
		utils.Log("error", "No repository found")

	case 1:
		if runtime.GOOS == "windows" {
			cmd := exec.Command(os.Getenv("COMSPEC"))
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Dir = reposFound[0].FullPath
			err := cmd.Start()
			if err == nil {
				cmd.Wait()
				os.Exit(0)
			}
		} else {
			shell := os.Getenv("SHELL")
			if shell == "" {
				shell = "/bin/sh"
			}

			utils.Log("cd", reposFound[0].FullPath)
			err := os.Chdir(reposFound[0].FullPath)
			utils.PanicIf(err)

			syscall.Exec(shell, []string{shell}, syscall.Environ())
		}

	default:
		utils.Log("error", "More than one repositories are found; Try more precise name")
		for _, repo := range reposFound {
			utils.Log("error", "- "+strings.Join(repo.PathParts, "/"))
		}
	}
}

func doImportStarred(c *cli.Context) {
	user := c.Args().First()
	doUpdate := c.Bool("update")
	isSSH := c.Bool("p")
	isShallow := c.Bool("shallow")

	if user == "" {
		cli.ShowCommandHelp(c, "starred")
		os.Exit(1)
	}

	githubToken := os.Getenv("GHQ_GITHUB_TOKEN")

	if githubToken == "" {
		var err error
		githubToken, err = GitConfigSingle("ghq.github.token")
		utils.PanicIf(err)
	}

	var client *github.Client

	if githubToken != "" {
		oauthTransport := &oauth.Transport{
			Token: &oauth.Token{AccessToken: githubToken},
		}
		client = github.NewClient(oauthTransport.Client())
	} else {
		client = github.NewClient(nil)
	}

	options := &github.ActivityListStarredOptions{Sort: "created"}

	for page := 1; ; page++ {
		options.Page = page

		repositories, res, err := client.Activity.ListStarred(user, options)
		utils.DieIf(err)

		utils.Log("page", fmt.Sprintf("%d/%d", page, res.LastPage))
		for _, repo := range repositories {
			url, err := url.Parse(*repo.HTMLURL)
			if err != nil {
				utils.Log("error", fmt.Sprintf("Could not parse URL <%s>: %s", repo.HTMLURL, err))
				continue
			}
			if isSSH {
				url, err = ConvertGitURLHTTPToSSH(url)
				if err != nil {
					utils.Log("error", fmt.Sprintf("Could not convert URL <%s>: %s", repo.HTMLURL, err))
					continue
				}
			}

			remote, err := NewRemoteRepository(url)
			if utils.ErrorIf(err) {
				continue
			}

			if remote.IsValid() == false {
				utils.Log("skip", fmt.Sprintf("Not a valid repository: %s", url))
				continue
			}

			getRemoteRepository(remote, doUpdate, isShallow)
		}

		if page >= res.LastPage {
			break
		}
	}
}

func doImportPocket(c *cli.Context) {
	doUpdate := c.Bool("update")
	isShallow := c.Bool("shallow")

	if pocket.ConsumerKey == "" {
		utils.Log("error", "Built without consumer key set")
		return
	}

	accessToken, err := GitConfigSingle("ghq.pocket.token")
	utils.PanicIf(err)

	if accessToken == "" {
		receiverURL, ch, err := pocket.StartAccessTokenReceiver()
		utils.PanicIf(err)

		utils.Log("pocket", "Waiting for Pocket authentication callback at "+receiverURL)

		utils.Log("pocket", "Obtaining request token")
		authRequest, err := pocket.ObtainRequestToken(receiverURL)
		utils.DieIf(err)

		url := pocket.GenerateAuthorizationURL(authRequest.Code, receiverURL)
		utils.Log("open", url)

		<-ch

		utils.Log("pocket", "Obtaining access token")
		authorized, err := pocket.ObtainAccessToken(authRequest.Code)
		utils.DieIf(err)

		utils.Log("authorized", authorized.Username)

		accessToken = authorized.AccessToken
		utils.Run("git", "config", "ghq.pocket.token", authorized.AccessToken)
	}

	utils.Log("pocket", "Retrieving github.com entries")
	res, err := pocket.RetrieveGitHubEntries(accessToken)
	utils.DieIf(err)

	for _, item := range res.List {
		url, err := url.Parse(item.ResolvedURL)
		if err != nil {
			utils.Log("error", fmt.Sprintf("Could not parse URL <%s>: %s", item.ResolvedURL, err))
			continue
		}

		remote, err := NewRemoteRepository(url)
		if utils.ErrorIf(err) {
			continue
		}

		if remote.IsValid() == false {
			utils.Log("skip", fmt.Sprintf("Not a valid repository: %s", url))
			continue
		}

		getRemoteRepository(remote, doUpdate, isShallow)
	}
}
