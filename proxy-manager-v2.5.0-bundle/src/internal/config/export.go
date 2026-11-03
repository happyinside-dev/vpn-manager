package config

func CleanLines(data []byte) ([]string, error) { return cleanYAMLLines(data) }
func SplitKV(s string) (string, string, bool)  { return splitKV(s) }
func Indent(s string) int                      { return indent(s) }
func BoolValue(s string) (bool, error)         { return boolValue(s) }
func IntValue(s string) (int, error)           { return intValue(s) }
