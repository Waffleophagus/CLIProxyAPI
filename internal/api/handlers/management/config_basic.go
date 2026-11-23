package management

import (
	"os"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func WriteConfig(path string, data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	var commented []config.ArchivedV8Section
	if len(doc.Content) > 0 && config.IsV8ConfigLayout(doc.Content[0]) {
		var errNorm error
		data, _, commented, errNorm = config.NormalizeConfigLayoutSilent(data, true)
		if errNorm != nil {
			return errNorm
		}
	}
	data = config.NormalizeCommentIndentation(data)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, errWrite := f.Write(data); errWrite != nil {
		_ = f.Close()
		return errWrite
	}
	if errSync := f.Sync(); errSync != nil {
		_ = f.Close()
		return errSync
	}
	if errClose := f.Close(); errClose != nil {
		return errClose
	}
	config.WarnUnrecognizedV8Sections(commented)
	return nil
}
