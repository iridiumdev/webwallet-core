package config

import (
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
	"sync"
)

type Config struct {
	Server Server `json:"server"`
	Mongo  Mongo  `json:"server"`
}

type Server struct {
	Address        string `json:"address"`
	StaticLocation string `json:"staticLocation"`
}

type Mongo struct {
	Address  string `json:"address"`
	Database string `json:"database"`
}

var singleton *Config
var once sync.Once

func Get() *Config {
	once.Do(func() {
		singleton = initConfig()

		viper.WatchConfig()
		viper.OnConfigChange(func(e fsnotify.Event) {
			fmt.Println("Config file changed:", e.Name)
			viper.Unmarshal(&singleton)
		})
	})

	return singleton
}

func initConfig() *Config {
	viper.SetConfigName("webwallet")      // name of conf file (without extension)
	viper.AddConfigPath("/etc/iridium/")  // path to look for the conf file in
	viper.AddConfigPath("$HOME/.iridium") // call multiple times to add many search paths
	viper.AddConfigPath(".")              // optionally look for conf in the working directory
	err := viper.ReadInConfig()           // Find and read the conf file

	if err != nil { // Handle errors reading the conf file
		panic(fmt.Errorf("Fatal error conf file: %s \n", err))
	}

	var conf Config
	viper.Unmarshal(&conf)

	fmt.Printf("Using config: %s\n", viper.ConfigFileUsed())

	return &conf
}
