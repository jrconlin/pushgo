package simplepush

import (
    "os"
    "io"
    "fmt"
    "bufio"
    "strings"
)

type config map[string]map[string]string
var configData config
//var readBuffer byte[]

func Config(file string) (config, error) {
    handle, err := os.Open(file)
    if err != nil {
        fmt.Println(err)
        return configData, err
    }

    if configData == nil {
        configData = make(config)
    }

    defer handle.Close()
    var section string
    reader := bufio.NewReader(handle)
    line, err := reader.ReadString('\n')
    for ;err == nil; line, err = reader.ReadString('\n') {
        line = strings.TrimSpace(line)
        switch {
            case len(line) == 0:
                continue
            case line[0] == '#':
                continue
        }
        if line[0] == '[' {
            section = strings.Trim(line, "[]")
            if configData[section] == nil {
                print(fmt.Sprintf("Creating section %s\n", section))
                configData[section] = make(map[string]string)
            }
            continue
        }
        if len(section) == 0 {
            continue
        }
        kv := strings.SplitN(line, "=", 2)
        configData[section][strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
        fmt.Printf("%s = %s\n",kv[0], configData[section][kv[0]])
    }
    if err != io.EOF {
        fmt.Println(fmt.Sprintf("Error: %s", err))
        return configData, err
    }
    err = nil
    return configData, err
}


func main() {
    data,_ := Config("test.conf")
    fmt.Println(data)
    fmt.Println(data["section"]["key2"])
}
