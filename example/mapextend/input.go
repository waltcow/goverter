package mapextend

// goverter:converter
type Converter interface {
	// goverter:mapExtend FullName ExtendFullName
	// goverter:mapExtend Age DefaultAge
	Convert(source *Input) *Output

	// goverter:mapExtend LastName FullName ExtendWithSpecName
	ConvertMeta(source *Input) *OutputMeta
}

type Input struct {
	ID        int
	FirstName string
	LastName  string
}
type Output struct {
	ID       int
	FullName string
	Age      int
}

type OutputMeta struct {
	Id       int
	FullName string
}

func ExtendFullName(source *Input) string {
	return source.FirstName + " " + source.LastName
}
func DefaultAge() int { return 42 }

func ExtendWithSpecName(name string) string {
	return name + " Spec"
}
