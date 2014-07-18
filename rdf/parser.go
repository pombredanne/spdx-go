package rdf

import (
	"fmt"
	"github.com/vladvelici/goraptor"
	"github.com/vladvelici/spdx-go/spdx"
	"io"
	"strings"
)

var (
	uri_nstype = uri("http://www.w3.org/1999/02/22-rdf-syntax-ns#type")

	typeDocument           = prefix("SpdxDocument")
	typeCreationInfo       = prefix("CreationInfo")
	typePackage            = prefix("Package")
	typeFile               = prefix("File")
	typeVerificationCode   = prefix("PackageVerificationCode")
	typeChecksum           = prefix("Checksum")
	typeArtifactOf         = prefix("doap:Project")
	typeReview             = prefix("Review")
	typeExtractedLicence   = prefix("ExtractedLicensingInfo")
	typeAnyLicence         = prefix("AnyLicenseInfo")
	typeConjunctiveSet     = prefix("ConjunctiveLicenseSet")
	typeDisjunctiveSet     = prefix("DisjunctiveLicenseSet")
	typeLicence            = prefix("License")
	typeAbstractLicenceSet = blank("abstractLicenceSet")
)

const (
	msgIncompatibleTypes    = "%s is already set to be type %s and cannot be changed to type %s."
	msgPropertyNotSupported = "Property %s is not supported for %s."
	msgAlreadyDefined       = "Property already defined."
	msgUnknownType          = "Found type %s which is unknown."
)

type abstractLicenceSet interface {
	Add(lic spdx.AnyLicence)
}

// Simple, one function call interface to parse a document
func Parse(input io.Reader, format string) (*spdx.Document, error) {
	parser := NewParser(input, format)
	defer parser.Free()
	return parser.Parse()
}

// Update a ValString pointer
func upd(ptr *spdx.ValueStr) updater {
	set := false
	return func(term goraptor.Term, meta *spdx.Meta) error {
		if set {
			return spdx.NewParseError(msgAlreadyDefined, meta)
		}

		ptr.Val = termStr(term)
		ptr.Meta = meta
		set = true
		return nil
	}
}

// Updates a ValString pointer, but cuts the prefix from the value
func updCutPrefix(prefix string, ptr *spdx.ValueStr) updater {
	set := false
	return func(term goraptor.Term, meta *spdx.Meta) error {
		if set {
			return spdx.NewParseError(msgAlreadyDefined, meta)
		}

		ptr.Val = strings.TrimPrefix(termStr(term), prefix)
		ptr.Meta = meta
		set = true
		return nil
	}
}

// Update a []ValString pointer
func updList(arr *[]spdx.ValueStr) updater {
	return func(term goraptor.Term, meta *spdx.Meta) error {
		*arr = append(*arr, spdx.Str(termStr(term), meta))
		return nil
	}
}

// Update a ValueCreator pointer
func updCreator(ptr *spdx.ValueCreator) updater {
	set := false
	return func(term goraptor.Term, meta *spdx.Meta) error {
		if set {
			return spdx.NewParseError(msgAlreadyDefined, meta)
		}
		ptr.SetValue(termStr(term))
		ptr.Meta = meta
		set = true
		return nil
	}
}

// Update a ValueDate pointer
func updDate(ptr *spdx.ValueDate) updater {
	set := false
	return func(term goraptor.Term, meta *spdx.Meta) error {
		if set {
			return spdx.NewParseError(msgAlreadyDefined, meta)
		}
		ptr.SetValue(termStr(term))
		ptr.Meta = meta
		set = true
		return nil
	}
}

// Update a []ValueCreator pointer
func updListCreator(arr *[]spdx.ValueCreator) updater {
	return func(term goraptor.Term, meta *spdx.Meta) error {
		*arr = append(*arr, spdx.NewValueCreator(termStr(term), meta))
		return nil
	}
}

type builder struct {
	t        goraptor.Term // type of element this builder represents
	ptr      interface{}   // the spdx element that this builder builds
	updaters map[string]updater
}

func (b *builder) apply(pred, obj goraptor.Term, meta *spdx.Meta) error {
	property := shortPrefix(pred)
	f, ok := b.updaters[property]
	if !ok {
		return spdx.NewParseError(fmt.Sprintf(msgPropertyNotSupported, property, b.t), meta)
	}
	return f(obj, meta)
}

func (b *builder) has(pred string) bool {
	_, ok := b.updaters[pred]
	return ok
}

type updater func(goraptor.Term, *spdx.Meta) error

type bufferEntry struct {
	*goraptor.Statement
	*spdx.Meta
}

type Parser struct {
	rdfparser *goraptor.Parser
	input     io.Reader
	index     map[string]*builder
	buffer    map[string][]bufferEntry
	doc       *spdx.Document
}

// This creates a goraptor.Parser object that needs to be freed after use.
// Call Parser.Free() after using the Parser.
func NewParser(input io.Reader, format string) *Parser {
	if format == "rdf" {
		format = "guess"
	}

	return &Parser{
		rdfparser: goraptor.NewParser(format),
		input:     input,
		index:     make(map[string]*builder),
		buffer:    make(map[string][]bufferEntry),
	}
}

// Parse the whole input stream and return the resulting spdx.Document or the first error that occurred.
func (p *Parser) Parse() (*spdx.Document, error) {
	ch := p.rdfparser.Parse(p.input, baseUri)
	locCh := p.rdfparser.LocatorChan()
	defer func() {
		// consume the channel if there's anything left.
		for _ = range ch {
			<-locCh
		}
	}()
	var err error
	for statement := range ch {
		locator := <-locCh
		meta := spdx.NewMetaL(locator.Line)
		if err = p.processTruple(statement, meta); err != nil {
			break
		}
	}
	for {
		_, ok := <-ch
		if !ok {
			break
		}
	}
	return p.doc, err
}

// Free the goraptor parser.
func (p *Parser) Free() {
	p.rdfparser.Free()
	p.doc = nil
}

func (p *Parser) setType(node, t goraptor.Term, meta *spdx.Meta) (interface{}, error) {
	nodeStr := termStr(node)
	bldr, ok := p.index[nodeStr]
	if ok {
		if !equalTypes(bldr.t, t) && bldr.has("ns:type") {
			//apply the type change
			if err := bldr.apply(uri("ns:type"), t, meta); err != nil {
				return nil, err
			}
			return bldr.ptr, nil
		}
		if !compatibleTypes(bldr.t, t) {
			return nil, spdx.NewParseError(fmt.Sprintf(msgIncompatibleTypes, node, bldr.t, t), meta)
		}
		return bldr.ptr, nil
	}

	// new builder by type
	switch {
	case t.Equals(typeDocument):
		p.doc = &spdx.Document{Meta: meta}
		bldr = p.documentMap(p.doc)
	case t.Equals(typeCreationInfo):
		bldr = p.creationInfoMap(&spdx.CreationInfo{Meta: meta})
	case t.Equals(typePackage):
		bldr = p.packageMap(&spdx.Package{Meta: meta})
	case t.Equals(typeChecksum):
		bldr = p.checksumMap(&spdx.Checksum{Meta: meta})
	case t.Equals(typeVerificationCode):
		bldr = p.verificationCodeMap(&spdx.VerificationCode{Meta: meta})
	case t.Equals(typeFile):
		bldr = p.fileMap(&spdx.File{Meta: meta})
	case t.Equals(typeReview):
		bldr = p.reviewMap(&spdx.Review{Meta: meta})
	case t.Equals(typeArtifactOf):
		artif := &spdx.ArtifactOf{Meta: meta}
		if artifUri, ok := node.(*goraptor.Uri); ok {
			artif.ProjectUri.Val = termStr(artifUri)
		}
		bldr = p.artifactOfMap(artif)
	case t.Equals(typeExtractedLicence):
		bldr = p.extractedLicensingInfoMap(&spdx.ExtractedLicence{Meta: meta})
	case t.Equals(typeAnyLicence):
		switch t := node.(type) {
		case *goraptor.Uri: // licence in spdx licence list
			bldr = p.licenceReferenceBuilder(node, meta)
		case *goraptor.Blank: // licence reference or abstract set
			if strings.HasPrefix(strings.ToLower(termStr(t)), "licenseref") {
				bldr = p.licenceReferenceBuilder(node, meta)
			} else {
				licList := make([]spdx.AnyLicence, 0)
				licSet := &spdx.LicenceSet{Members: licList, Meta: meta}
				bldr = p.licenceSetMap(licSet)
			}
		}
	case t.Equals(typeConjunctiveSet):
		bldr = p.conjunctiveSetBuilder(meta)
	case t.Equals(typeDisjunctiveSet):
		bldr = p.disjuntiveSetBuilder(meta)
	default:
		return nil, spdx.NewParseError(fmt.Sprintf(msgUnknownType, t), meta)
	}

	p.index[nodeStr] = bldr

	// run buffer
	buf := p.buffer[nodeStr]
	for _, stm := range buf {
		if err := bldr.apply(stm.Predicate, stm.Object, stm.Meta); err != nil {
			return nil, err
		}
	}
	delete(p.buffer, nodeStr)

	return bldr.ptr, nil
}

func (p *Parser) processTruple(stm *goraptor.Statement, meta *spdx.Meta) error {
	node := termStr(stm.Subject)
	if stm.Predicate.Equals(uri_nstype) {
		_, err := p.setType(stm.Subject, stm.Object, meta)
		return err
	}

	// apply function if it's a builder
	bldr, ok := p.index[node]
	if ok {
		return bldr.apply(stm.Predicate, stm.Object, meta)
	}

	// buffer statement
	if _, ok := p.buffer[node]; !ok {
		p.buffer[node] = make([]bufferEntry, 0)
	}
	p.buffer[node] = append(p.buffer[node], bufferEntry{stm, meta})

	return nil
}

// Parser.req* functions are supposded to get the node from either the index or the buffer,
// check if it's the required type and return a pointer to the relevant spdx.* object.

// Checks if found is any of the need types.
func equalTypes(found goraptor.Term, need ...goraptor.Term) bool {
	for _, b := range need {
		if found == b || found.Equals(b) {
			return true
		}
	}
	return false
}

// Checks if found is the same as need.
//
// If need is any of typeLicence, typeLicence, typeDisjunctiveSet,
// typeConjunctiveSet and typeExtractedLicence and found is AnyLicence, it
// is permitted and the function returns true.
func compatibleTypes(found, need goraptor.Term) bool {
	if equalTypes(found, need) {
		return true
	}
	if equalTypes(need, typeAnyLicence) {
		return equalTypes(found, typeExtractedLicence, typeConjunctiveSet, typeDisjunctiveSet, typeLicence)
	}
	return false
}

func (p *Parser) reqType(node, t goraptor.Term) (interface{}, error) {
	bldr, ok := p.index[termStr(node)]
	if ok {
		if !compatibleTypes(bldr.t, t) {
			return nil, fmt.Errorf(msgIncompatibleTypes, node, bldr.t, t)
		}
		return bldr.ptr, nil
	}
	return p.setType(node, t, nil)
}

func (p *Parser) reqDocument(node goraptor.Term) (*spdx.Document, error) {
	obj, err := p.reqType(node, typeDocument)
	return obj.(*spdx.Document), err
}
func (p *Parser) reqCreationInfo(node goraptor.Term) (*spdx.CreationInfo, error) {
	obj, err := p.reqType(node, typeCreationInfo)
	return obj.(*spdx.CreationInfo), err
}
func (p *Parser) reqPackage(node goraptor.Term) (*spdx.Package, error) {
	obj, err := p.reqType(node, typePackage)
	return obj.(*spdx.Package), err
}
func (p *Parser) reqFile(node goraptor.Term) (*spdx.File, error) {
	obj, err := p.reqType(node, typeFile)
	return obj.(*spdx.File), err
}
func (p *Parser) reqVerificationCode(node goraptor.Term) (*spdx.VerificationCode, error) {
	obj, err := p.reqType(node, typeVerificationCode)
	return obj.(*spdx.VerificationCode), err
}
func (p *Parser) reqChecksum(node goraptor.Term) (*spdx.Checksum, error) {
	obj, err := p.reqType(node, typeChecksum)
	return obj.(*spdx.Checksum), err
}
func (p *Parser) reqReview(node goraptor.Term) (*spdx.Review, error) {
	obj, err := p.reqType(node, typeReview)
	return obj.(*spdx.Review), err
}
func (p *Parser) reqExtractedLicence(node goraptor.Term) (*spdx.ExtractedLicence, error) {
	obj, err := p.reqType(node, typeExtractedLicence)
	return obj.(*spdx.ExtractedLicence), err
}
func (p *Parser) reqAnyLicence(node goraptor.Term) (spdx.AnyLicence, error) {
	obj, err := p.reqType(node, typeAnyLicence)
	if err != nil {
		return nil, err
	}
	switch lic := obj.(type) {
	case *spdx.AnyLicence:
		return *lic, nil
	case *spdx.ConjunctiveLicenceSet:
		return *lic, nil
	case *spdx.DisjunctiveLicenceSet:
		return *lic, nil
	case *[]spdx.AnyLicence:
		return nil, nil
	case *spdx.Licence:
		return *lic, nil
	case *spdx.ExtractedLicence:
		return lic, nil
	default:
		return nil, fmt.Errorf("Unexpected error, fix rdf parser. %s || %#v", node, obj)
	}
}
func (p *Parser) reqArtifactOf(node goraptor.Term) (*spdx.ArtifactOf, error) {
	obj, err := p.reqType(node, typeArtifactOf)
	return obj.(*spdx.ArtifactOf), err
}

func (p *Parser) documentMap(doc *spdx.Document) *builder {
	bldr := &builder{t: typeDocument, ptr: doc}
	bldr.updaters = map[string]updater{
		"specVersion":  upd(&doc.SpecVersion),
		"dataLicense":  updCutPrefix(licenceUri, &doc.DataLicence),
		"rdfs:comment": upd(&doc.Comment),
		"creationInfo": func(obj goraptor.Term, meta *spdx.Meta) error {
			cri, err := p.reqCreationInfo(obj)
			doc.CreationInfo = cri
			return err
		},
		"describesPackage": func(obj goraptor.Term, meta *spdx.Meta) error {
			pkg, err := p.reqPackage(obj)
			if err != nil {
				return err
			}
			if doc.Packages == nil {
				doc.Packages = []*spdx.Package{pkg}
			} else {
				doc.Packages = append(doc.Packages, pkg)
			}
			return nil
		},
		"referencesFile": func(obj goraptor.Term, meta *spdx.Meta) error {
			file, err := p.reqFile(obj)
			if err != nil {
				return err
			}
			if doc.Files == nil {
				doc.Files = []*spdx.File{file}
			} else {
				doc.Files = append(doc.Files, file)
			}
			return nil
		},
		"reviewed": func(obj goraptor.Term, meta *spdx.Meta) error {
			rev, err := p.reqReview(obj)
			if err != nil {
				return err
			}
			if doc.Reviews == nil {
				doc.Reviews = []*spdx.Review{rev}
			} else {
				doc.Reviews = append(doc.Reviews, rev)
			}
			return nil
		},
		"hasExtractedLicensingInfo": func(obj goraptor.Term, meta *spdx.Meta) error {
			lic, err := p.reqExtractedLicence(obj)
			if err != nil {
				return err
			}
			if doc.ExtractedLicences == nil {
				doc.ExtractedLicences = []*spdx.ExtractedLicence{lic}
			} else {
				doc.ExtractedLicences = append(doc.ExtractedLicences, lic)
			}
			return nil
		},
	}

	return bldr
}

func (p *Parser) creationInfoMap(cri *spdx.CreationInfo) *builder {
	bldr := &builder{t: typeCreationInfo, ptr: cri}
	bldr.updaters = map[string]updater{
		"creator":            updListCreator(&cri.Creator),
		"rdfs:comment":       upd(&cri.Comment),
		"created":            updDate(&cri.Created),
		"licenseListVersion": upd(&cri.LicenceListVersion),
	}
	return bldr
}

func (p *Parser) reviewMap(rev *spdx.Review) *builder {
	bldr := &builder{t: typeReview, ptr: rev}
	bldr.updaters = map[string]updater{
		"reviewer":     updCreator(&rev.Reviewer),
		"rdfs:comment": upd(&rev.Comment),
		"reviewDate":   updDate(&rev.Date),
	}
	return bldr

}

func (p *Parser) packageMap(pkg *spdx.Package) *builder {
	bldr := &builder{t: typePackage, ptr: pkg}
	bldr.updaters = map[string]updater{
		"name":             upd(&pkg.Name),
		"versionInfo":      upd(&pkg.Version),
		"packageFileName":  upd(&pkg.FileName),
		"supplier":         updCreator(&pkg.Supplier),
		"originator":       updCreator(&pkg.Originator),
		"downloadLocation": upd(&pkg.DownloadLocation),
		"packageVerificationCode": func(obj goraptor.Term, meta *spdx.Meta) error {
			vc, err := p.reqVerificationCode(obj)
			pkg.VerificationCode = vc
			return err
		},
		"checksum": func(obj goraptor.Term, meta *spdx.Meta) error {
			cksum, err := p.reqChecksum(obj)
			pkg.Checksum = cksum
			return err
		},
		"doap:homepage": upd(&pkg.HomePage),
		"sourceInfo":    upd(&pkg.SourceInfo),
		"licenseConcluded": func(obj goraptor.Term, meta *spdx.Meta) error {
			lic, err := p.reqAnyLicence(obj)
			pkg.LicenceConcluded = lic
			return err
		},
		"licenseInfoFromFiles": func(obj goraptor.Term, meta *spdx.Meta) error {
			lic, err := p.reqAnyLicence(obj)
			if err != nil {
				return err
			}
			if pkg.LicenceInfoFromFiles == nil {
				pkg.LicenceInfoFromFiles = []spdx.AnyLicence{lic}
			} else {
				pkg.LicenceInfoFromFiles = append(pkg.LicenceInfoFromFiles, lic)
			}
			return nil
		},
		"licenseDeclared": func(obj goraptor.Term, meta *spdx.Meta) error {
			lic, err := p.reqAnyLicence(obj)
			pkg.LicenceDeclared = lic
			return err
		},
		"licenseComments": upd(&pkg.LicenceComments),
		"copyrightText":   upd(&pkg.CopyrightText),
		"summary":         upd(&pkg.Summary),
		"description":     upd(&pkg.Description),
		"hasFile": func(obj goraptor.Term, meta *spdx.Meta) error {
			file, err := p.reqFile(obj)
			if err != nil {
				return err
			}
			if pkg.Files == nil {
				pkg.Files = []*spdx.File{file}
			} else {
				pkg.Files = append(pkg.Files, file)
			}
			return nil
		},
	}
	return bldr
}

func (p *Parser) checksumMap(cksum *spdx.Checksum) *builder {
	bldr := &builder{t: typeChecksum, ptr: cksum}
	algoSet := false
	bldr.updaters = map[string]updater{
		"algorithm": func(obj goraptor.Term, meta *spdx.Meta) error {
			if algoSet {
				return spdx.NewParseError(msgAlreadyDefined, meta)
			}
			str := termStr(obj)
			str = strings.Replace(str, "http://spdx.org/rdf/terms#checksumAlgorithm_", "", 1)
			str = strings.ToUpper(str)
			cksum.Algo.Val, cksum.Algo.Meta = str, meta
			algoSet = true
			return nil
		},
		"checksumValue": upd(&cksum.Value),
	}
	return bldr
}

func (p *Parser) verificationCodeMap(vc *spdx.VerificationCode) *builder {
	bldr := &builder{t: typeVerificationCode, ptr: vc}
	bldr.updaters = map[string]updater{
		"packageVerificationCodeValue":        upd(&vc.Value),
		"packageVerificationCodeExcludedFile": updList(&vc.ExcludedFiles),
	}
	return bldr
}

func (p *Parser) fileMap(file *spdx.File) *builder {
	bldr := &builder{t: typeFile, ptr: file}
	bldr.updaters = map[string]updater{
		"fileName":     upd(&file.Name),
		"rdfs:comment": upd(&file.Comment),
		"fileType":     updCutPrefix("http://spdx.org/rdf/terms#", &file.Type),
		"checksum": func(obj goraptor.Term, meta *spdx.Meta) error {
			cksum, err := p.reqChecksum(obj)
			file.Checksum = cksum
			return err
		},
		"copyrightText": upd(&file.CopyrightText),
		"noticeText":    upd(&file.Notice),
		"licenseConcluded": func(obj goraptor.Term, meta *spdx.Meta) error {
			lic, err := p.reqAnyLicence(obj)
			file.LicenceConcluded = lic
			return err
		},
		"licenseInfoInFile": func(obj goraptor.Term, meta *spdx.Meta) error {
			lic, err := p.reqAnyLicence(obj)
			if err != nil {
				return err
			}
			if file.LicenceInfoInFile == nil {
				file.LicenceInfoInFile = []spdx.AnyLicence{lic}
			} else {
				file.LicenceInfoInFile = append(file.LicenceInfoInFile, lic)
			}
			return nil
		},
		"licenseComments": upd(&file.LicenceComments),
		"fileContributor": updList(&file.Contributor),
		"fileDependency": func(obj goraptor.Term, meta *spdx.Meta) error {
			f, err := p.reqFile(obj)
			if err != nil {
				return err
			}
			if file.Dependency == nil {
				file.Dependency = []*spdx.File{f}
			} else {
				file.Dependency = append(file.Dependency, f)
			}
			return nil
		},
		"artifactOf": func(obj goraptor.Term, meta *spdx.Meta) error {
			artif, err := p.reqArtifactOf(obj)
			if err != nil {
				return err
			}
			if file.ArtifactOf == nil {
				file.ArtifactOf = []*spdx.ArtifactOf{artif}
			} else {
				file.ArtifactOf = append(file.ArtifactOf, artif)
			}
			return nil
		},
	}
	return bldr
}

func (p *Parser) artifactOfMap(artif *spdx.ArtifactOf) *builder {
	bldr := &builder{t: typeArtifactOf, ptr: artif}
	bldr.updaters = map[string]updater{
		"doap:name":     upd(&artif.Name),
		"doap:homepage": upd(&artif.HomePage),
	}
	return bldr
}

func (p *Parser) extractedLicensingInfoMap(lic *spdx.ExtractedLicence) *builder {
	bldr := &builder{t: typeExtractedLicence, ptr: lic}
	bldr.updaters = map[string]updater{
		"licenseId":     upd(&lic.Id),
		"name":          updList(&lic.Name),
		"extractedText": upd(&lic.Text),
		"rdfs:comment":  upd(&lic.Comment),
		"rdfs:seeAlso":  updList(&lic.CrossReference),
	}
	return bldr
}

func (p *Parser) licenceSetMap(set abstractLicenceSet) *builder {
	bldr := &builder{t: typeAbstractLicenceSet, ptr: set}
	bldr.updaters = map[string]updater{
		"member": func(obj goraptor.Term, meta *spdx.Meta) error {
			lic, err := p.reqAnyLicence(obj)
			if err != nil {
				return err
			}
			set.Add(lic)
			return nil
		},
		"ns:type": func(obj goraptor.Term, meta *spdx.Meta) error {
			if !equalTypes(bldr.t, typeAbstractLicenceSet) {
				return spdx.NewParseError(msgAlreadyDefined, meta)
			}
			tmpSet := set.(*spdx.LicenceSet)
			goodMeta := tmpSet.Meta
			if goodMeta == nil {
				goodMeta = meta
			}
			if equalTypes(obj, typeConjunctiveSet) {
				bldr.t = typeConjunctiveSet
				conj := spdx.NewConjunctiveSet(goodMeta, tmpSet.Members...)
				set = &conj
			} else if equalTypes(obj, typeDisjunctiveSet) {
				bldr.t = typeDisjunctiveSet
				disj := spdx.NewDisjunctiveSet(goodMeta, tmpSet.Members...)
				set = &disj
			} else {
				return spdx.NewParseError(fmt.Sprintf(msgIncompatibleTypes, "Licence Set", bldr.t, obj), meta)
			}
			return nil
		},
	}
	return bldr
}

func (p *Parser) conjunctiveSetBuilder(meta *spdx.Meta) *builder {
	set := spdx.NewConjunctiveSet(meta, make([]spdx.AnyLicence, 0)...)
	bldr := p.licenceSetMap(&set)
	bldr.t = typeConjunctiveSet
	return bldr
}

func (p *Parser) disjuntiveSetBuilder(meta *spdx.Meta) *builder {
	set := spdx.NewDisjunctiveSet(meta, make([]spdx.AnyLicence, 0)...)
	bldr := p.licenceSetMap(&set)
	bldr.t = typeDisjunctiveSet
	return bldr
}

func licenceReferenceTerm(node goraptor.Term, meta *spdx.Meta) *spdx.Licence {
	str := strings.TrimPrefix(termStr(node), licenceUri)
	lic := spdx.NewLicence(str, meta)
	return &lic
}

func (p *Parser) licenceReferenceBuilder(node goraptor.Term, meta *spdx.Meta) *builder {
	lic := licenceReferenceTerm(node, meta)
	return &builder{t: typeLicence, ptr: lic}
}
