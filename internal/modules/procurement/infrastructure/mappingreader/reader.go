package mappingreader

import (
	mappingdomain "github.com/XManCodeing/XMall/internal/modules/catalog/mapping/domain"
	procurementcontract "github.com/XManCodeing/XMall/internal/modules/procurement/contract"
)

type ProductSource interface {
	GetByLocalProductID(productID uint) (*mappingdomain.Mapping, error)
}

type SKUReaderSource interface {
	GetByLocalSKUID(skuID uint) (*mappingdomain.SKUMapping, error)
}

type ProductReader struct{ source ProductSource }
type SKUReader struct{ source SKUReaderSource }

var _ procurementcontract.ProductMappingReader = (*ProductReader)(nil)
var _ procurementcontract.SKUMappingReader = (*SKUReader)(nil)

func NewProducts(source ProductSource) *ProductReader { return &ProductReader{source: source} }
func NewSKUs(source SKUReaderSource) *SKUReader       { return &SKUReader{source: source} }

func (r *ProductReader) FindConnectionID(productID uint) (uint, bool, error) {
	mapping, err := r.source.GetByLocalProductID(productID)
	if err != nil || mapping == nil {
		return 0, false, err
	}
	return mapping.ConnectionID, true, nil
}

func (r *ProductReader) FindProductExternalID(productID uint) (string, bool, error) {
	mapping, err := r.source.GetByLocalProductID(productID)
	if err != nil || mapping == nil {
		return "", false, err
	}
	return mapping.UpstreamProductExternalID, true, nil
}

func (r *SKUReader) FindSKUExternalID(skuID uint) (string, bool, error) {
	mapping, err := r.source.GetByLocalSKUID(skuID)
	if err != nil || mapping == nil {
		return "", false, err
	}
	if mapping.ProductLevel {
		return "", true, nil
	}
	return mapping.UpstreamSKUExternalID, true, nil
}

func (r *SKUReader) FindUpstreamSKUID(skuID uint) (uint, bool, error) {
	mapping, err := r.source.GetByLocalSKUID(skuID)
	if err != nil || mapping == nil {
		return 0, false, err
	}
	return mapping.UpstreamSKUID, true, nil
}
