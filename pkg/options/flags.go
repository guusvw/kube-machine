package options

import (
	"strconv"
	"strings"

	"github.com/docker/machine/libmachine/drivers"
	"k8s.io/client-go/pkg/api/v1"
)

var AnnotationPrefix = "node.k8s.io/"

type AnnotationOptions struct {
	annotations map[string]string
}

func New(node v1.Node) drivers.DriverOptions {
	return AnnotationOptions{annotations: node.GetAnnotations()}
}

func (n AnnotationOptions) String(key string) string {
	return n.annotations[AnnotationPrefix+key]
}

func (n AnnotationOptions) StringSlice(key string) []string {
	a := n.annotations[AnnotationPrefix+key]
	if a == "" {
		return []string{}
	}

	return strings.Split(a, ",")
}

func (n AnnotationOptions) Int(key string) int {
	a := n.annotations[AnnotationPrefix+key]
	i, _ := strconv.Atoi(a)
	return i
}

func (n AnnotationOptions) Bool(key string) bool {
	b, _ := strconv.ParseBool(n.annotations[AnnotationPrefix+key])
	return b
}
