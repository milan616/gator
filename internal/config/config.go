package config

import (
    "encoding/json"
    "os"
    "path/filepath"
)

const CONFFILENAME = ".gatorconfig.json"

type Config struct {
    DBURL           string `json:"db_url"`
    CurrentUserName string `json:"current_user_name"`
}

func Read() (Config, error) {
    var cfg Config
    home, err := os.UserHomeDir()
    if err != nil {
        return cfg, err
    }
    path := filepath.Join(home, CONFFILENAME)

    data, err := os.ReadFile(path)
    if err != nil {
        return cfg, err
    }

    err = json.Unmarshal(data, &cfg)
    if err != nil {
        return cfg, err
    }

    return cfg, nil
}

func (c *Config) SetUser(currentUser string) error {
    c.CurrentUserName = currentUser

    output, err := json.Marshal(c)
    if err != nil {
        return err
    }
    
    home, err := os.UserHomeDir()
    if err != nil {
        return err
    }
    path := filepath.Join(home, CONFFILENAME)

    err = os.WriteFile(path, output, 0666)
    if err != nil {
        return err
    }
    return nil
}
