package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"text/tabwriter"

	core "github.com/ipfs/go-ipfs/core"
	cmdenv "github.com/ipfs/go-ipfs/core/commands/cmdenv"
	corerepo "github.com/ipfs/go-ipfs/core/corerepo"
	fsrepo "github.com/ipfs/go-ipfs/repo/fsrepo"

	cmds "gx/ipfs/QmQkW9fnCsg9SLHdViiAh6qfBppodsPZVpU92dZLqYtEfs/go-ipfs-cmds"
	cid "gx/ipfs/QmTbxNB1NwDesLmKTscr4udL2tVP7MaxvXnD1D9yX7g3PN/go-cid"
	config "gx/ipfs/QmUAuYuiafnJRZxDDX7MuruMNsicYNuyub5vUeAcupUBNs/go-ipfs-config"
	ds "gx/ipfs/QmUadX5EcvrBmxAV9sE7wUWtWSqxns5K84qKJBixmcT1w9/go-datastore"
	b58 "gx/ipfs/QmWFAMPqsEyUX7gDUsRVmMWz59FxSpJ1b2v6bJ1yYzo7jY/go-base58-fast/base58"
	bstore "gx/ipfs/QmXjKkjMDTtXAiLBwstVexofB8LeruZmE2eBd85GwGFFLA/go-ipfs-blockstore"
	cmdkit "gx/ipfs/Qmde5VP1qUkyQXKCfmEUA7bP64V2HAptbJ7phuPp7jXWwg/go-ipfs-cmdkit"
)

type RepoVersion struct {
	Version string
}

var RepoCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Manipulate the IPFS repo.",
		ShortDescription: `
'ipfs repo' is a plumbing command used to manipulate the repo.
`,
	},

	Subcommands: map[string]*cmds.Command{
		"stat":    repoStatCmd,
		"gc":      repoGcCmd,
		"fsck":    repoFsckCmd,
		"version": repoVersionCmd,
		"verify":  repoVerifyCmd,
		"rm-root": repoRmRootCmd,
	},
}

// GcResult is the result returned by "repo gc" command.
type GcResult struct {
	Key   cid.Cid
	Error string `json:",omitempty"`
}

const (
	repoStreamErrorsOptionName = "stream-errors"
	repoQuietOptionName        = "quiet"
)

var repoGcCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Perform a garbage collection sweep on the repo.",
		ShortDescription: `
'ipfs repo gc' is a plumbing command that will sweep the local
set of stored objects and remove ones that are not pinned in
order to reclaim hard disk space.
`,
	},
	Options: []cmdkit.Option{
		cmdkit.BoolOption(repoStreamErrorsOptionName, "Stream errors."),
		cmdkit.BoolOption(repoQuietOptionName, "q", "Write minimal output."),
	},
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) error {
		n, err := cmdenv.GetNode(env)
		if err != nil {
			return err
		}

		streamErrors, _ := req.Options[repoStreamErrorsOptionName].(bool)

		gcOutChan := corerepo.GarbageCollectAsync(n, req.Context)

		if streamErrors {
			errs := false
			for res := range gcOutChan {
				if res.Error != nil {
					if err := re.Emit(&GcResult{Error: res.Error.Error()}); err != nil {
						return err
					}
					errs = true
				} else {
					if err := re.Emit(&GcResult{Key: res.KeyRemoved}); err != nil {
						return err
					}
				}
			}
			if errs {
				return errors.New("encountered errors during gc run")
			}
		} else {
			err := corerepo.CollectResult(req.Context, gcOutChan, func(k cid.Cid) {
				re.Emit(&GcResult{Key: k})
			})
			if err != nil {
				return err
			}
		}

		return nil
	},
	Type: GcResult{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, gcr *GcResult) error {
			quiet, _ := req.Options[repoQuietOptionName].(bool)

			if gcr.Error != "" {
				_, err := fmt.Fprintf(w, "Error: %s\n", gcr.Error)
				return err
			}

			prefix := "removed "
			if quiet {
				prefix = ""
			}

			_, err := fmt.Fprintf(w, "%s%s\n", prefix, gcr.Key)
			return err
		}),
	},
}

const (
	repoSizeOnlyOptionName = "size-only"
	repoHumanOptionName    = "human"
)

var repoStatCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Get stats for the currently used repo.",
		ShortDescription: `
'ipfs repo stat' provides information about the local set of
stored objects. It outputs:

RepoSize        int Size in bytes that the repo is currently taking.
StorageMax      string Maximum datastore size (from configuration)
NumObjects      int Number of objects in the local repo.
RepoPath        string The path to the repo being currently used.
Version         string The repo version.
`,
	},
	Options: []cmdkit.Option{
		cmdkit.BoolOption(repoSizeOnlyOptionName, "Only report RepoSize and StorageMax."),
		cmdkit.BoolOption(repoHumanOptionName, "Output sizes in MiB."),
	},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		n, err := cmdenv.GetNode(env)
		if err != nil {
			return err
		}

		sizeOnly, _ := req.Options[repoSizeOnlyOptionName].(bool)
		if sizeOnly {
			sizeStat, err := corerepo.RepoSize(req.Context, n)
			if err != nil {
				return err
			}
			cmds.EmitOnce(res, &corerepo.Stat{
				SizeStat: sizeStat,
			})
			return nil
		}

		stat, err := corerepo.RepoStat(req.Context, n)
		if err != nil {
			return err
		}

		return cmds.EmitOnce(res, &stat)
	},
	Type: &corerepo.Stat{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, stat *corerepo.Stat) error {
			wtr := tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)
			defer wtr.Flush()

			human, _ := req.Options[repoHumanOptionName].(bool)
			sizeOnly, _ := req.Options[repoSizeOnlyOptionName].(bool)

			printSize := func(name string, size uint64) {
				sizeInMiB := size / (1024 * 1024)
				if human && sizeInMiB > 0 {
					fmt.Fprintf(wtr, "%s (MiB):\t%d\n", name, sizeInMiB)
				} else {
					fmt.Fprintf(wtr, "%s:\t%d\n", name, size)
				}
			}

			if !sizeOnly {
				fmt.Fprintf(wtr, "NumObjects:\t%d\n", stat.NumObjects)
			}

			printSize("RepoSize", stat.RepoSize)
			printSize("StorageMax", stat.StorageMax)

			if !sizeOnly {
				fmt.Fprintf(wtr, "RepoPath:\t%s\n", stat.RepoPath)
				fmt.Fprintf(wtr, "Version:\t%s\n", stat.Version)
			}

			return nil
		}),
	},
}

var repoFsckCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Remove repo lockfiles.",
		ShortDescription: `
'ipfs repo fsck' is a plumbing command that will remove repo and level db
lockfiles, as well as the api file. This command can only run when no ipfs
daemons are running.
`,
	},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		configRoot, err := cmdenv.GetConfigRoot(env)
		if err != nil {
			return err
		}

		dsPath, err := config.DataStorePath(configRoot)
		if err != nil {
			return err
		}

		dsLockFile := filepath.Join(dsPath, "LOCK") // TODO: get this lockfile programmatically
		repoLockFile := filepath.Join(configRoot, fsrepo.LockFile)
		apiFile := filepath.Join(configRoot, "api") // TODO: get this programmatically

		log.Infof("Removing repo lockfile: %s", repoLockFile)
		log.Infof("Removing datastore lockfile: %s", dsLockFile)
		log.Infof("Removing api file: %s", apiFile)

		err = os.Remove(repoLockFile)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		err = os.Remove(dsLockFile)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		err = os.Remove(apiFile)
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		return cmds.EmitOnce(res, &MessageOutput{"Lockfiles have been removed.\n"})
	},
	Type: MessageOutput{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, out *MessageOutput) error {
			fmt.Fprintf(w, out.Message)
			return nil
		}),
	},
}

var repoRmRootCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Unlink the root used by the files API.",
		ShortDescription: `
'ipfs repo rm-root' will unlink the root used by the files API ('ipfs
files' commands) without trying to read the root itself.  The root and
its children will be removed the next time the garbage collector runs,
unless pinned.

This command is designed to recover form the situation when the root
becomes unavailable and recovering it (such as recreating it, or
fetching it from the network) is not possible.  This command should
only be used as a last resort as using this command could lead to data
loss if there are unpinned nodes connected to the root.

This command can only run when the ipfs daemon is not running.
`,
	},
	Options: []cmdkit.Option{
		cmdkit.BoolOption("confirm", "Really perform operation."),
		cmdkit.BoolOption("remove-existing-root", "Remove even if it root exists locally."),
	},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		confirm, _ := req.Options["confirm"].(bool)
		removeExistingRoot, _ := req.Options["remove-existing-root"].(bool)

		if !confirm {
			return fmt.Errorf("this is a potentially dangerous operation please pass --confirm to proceed")
		}

		configRoot, err := cmdenv.GetConfigRoot(env)
		if err != nil {
			return err
		}

		// Can't use a full node as that interferes with the removal
		// of the files root, so open the repo directly
		repo, err := fsrepo.Open(configRoot)
		if err != nil {
			return err
		}
		defer repo.Close()
		bs := bstore.NewBlockstore(repo.Datastore())

		// Get the old root and display it to the user so that they can
		// can do something to prevent from being garbage collected,
		// such as pin it
		val, err := repo.Datastore().Get(core.FilesRootKey)
		if err == ds.ErrNotFound || val == nil {
			return cmds.EmitOnce(res, &MessageOutput{"Files API root not found.\n"})
		}

		var cidStr string
		var have bool
		c, err := cid.Cast(val)
		if err == nil {
			cidStr = c.String()
			have, _ = bs.Has(c)
		} else {
			cidStr = b58.Encode(val)
		}

		if have && !removeExistingRoot {
			return fmt.Errorf("root %s exists locally. Are you sure you want to unlink this? Pass --remove-existing-root to continue", cidStr)
		}

		err = repo.Datastore().Delete(core.FilesRootKey)
		if err != nil {
			return fmt.Errorf("unable to remove API root: %s.  Root hash was %s", err.Error(), cidStr)
		}
		return cmds.EmitOnce(res, &MessageOutput{
			fmt.Sprintf("Unlinked files API root.  Root hash was %s.\n", cidStr),
		})
	},
	Type: MessageOutput{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, msg *MessageOutput) error {
			_, err := fmt.Fprintf(w, "%s", msg.Message)
			return err
		}),
	},
}

type VerifyProgress struct {
	Msg      string
	Progress int
}

func verifyWorkerRun(ctx context.Context, wg *sync.WaitGroup, keys <-chan cid.Cid, results chan<- string, bs bstore.Blockstore) {
	defer wg.Done()

	for k := range keys {
		_, err := bs.Get(k)
		if err != nil {
			select {
			case results <- fmt.Sprintf("block %s was corrupt (%s)", k, err):
			case <-ctx.Done():
				return
			}

			continue
		}

		select {
		case results <- "":
		case <-ctx.Done():
			return
		}
	}
}

func verifyResultChan(ctx context.Context, keys <-chan cid.Cid, bs bstore.Blockstore) <-chan string {
	results := make(chan string)

	go func() {
		defer close(results)

		var wg sync.WaitGroup

		for i := 0; i < runtime.NumCPU()*2; i++ {
			wg.Add(1)
			go verifyWorkerRun(ctx, &wg, keys, results, bs)
		}

		wg.Wait()
	}()

	return results
}

var repoVerifyCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Verify all blocks in repo are not corrupted.",
	},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		nd, err := cmdenv.GetNode(env)
		if err != nil {
			return err
		}

		bs := bstore.NewBlockstore(nd.Repo.Datastore())
		bs.HashOnRead(true)

		keys, err := bs.AllKeysChan(req.Context)
		if err != nil {
			log.Error(err)
			return err
		}

		results := verifyResultChan(req.Context, keys, bs)

		var fails int
		var i int
		for msg := range results {
			if msg != "" {
				if err := res.Emit(&VerifyProgress{Msg: msg}); err != nil {
					return err
				}
				fails++
			}
			i++
			if err := res.Emit(&VerifyProgress{Progress: i}); err != nil {
				return err
			}
		}

		if fails != 0 {
			return errors.New("verify complete, some blocks were corrupt")
		}

		return res.Emit(&VerifyProgress{Msg: "verify complete, all blocks validated."})
	},
	Type: &VerifyProgress{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, obj *VerifyProgress) error {
			if strings.Contains(obj.Msg, "was corrupt") {
				fmt.Fprintln(os.Stdout, obj.Msg)
				return nil
			}

			if obj.Msg != "" {
				if len(obj.Msg) < 20 {
					obj.Msg += "             "
				}
				fmt.Fprintln(w, obj.Msg)
				return nil
			}

			fmt.Fprintf(w, "%d blocks processed.\r", obj.Progress)
			return nil
		}),
	},
}

var repoVersionCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Show the repo version.",
		ShortDescription: `
'ipfs repo version' returns the current repo version.
`,
	},

	Options: []cmdkit.Option{
		cmdkit.BoolOption(repoQuietOptionName, "q", "Write minimal output."),
	},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		return cmds.EmitOnce(res, &RepoVersion{
			Version: fmt.Sprint(fsrepo.RepoVersion),
		})
	},
	Type: RepoVersion{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, out *RepoVersion) error {
			quiet, _ := req.Options[repoQuietOptionName].(bool)

			if quiet {
				fmt.Fprintf(w, fmt.Sprintf("fs-repo@%s\n", out.Version))
			} else {
				fmt.Fprintf(w, fmt.Sprintf("ipfs repo version fs-repo@%s\n", out.Version))
			}
			return nil
		}),
	},
}
