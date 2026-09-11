package main

import (
	"bytes"
	"errors"
	"image"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func main(){
	rounter := gin.Default()
	rounter.MaxMultipartMemory = 16 << 20
	
	rounter.POST("/compress", compressHardler)

	rounter.Run(":46939")
}

func compressHardler(c *gin.Context){
	file, err := c.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error":err.Error()})
		return
	}
	
	log.Println(file.Filename)

	f, err := file.Open()
	if err != nil{
		c.JSON(http.StatusBadRequest, gin.H{"error":err.Error()})
		return 
	}
	defer f.Close()

	src, err := io.ReadAll(io.LimitReader(f, 8<< 20))
	if err != nil{
		c.JSON(http.StatusBadRequest, gin.H{"error":err.Error()})
		return
	}
	
	scaleStr := c.DefaultQuery("scale", "1.0")
	if scaleStr != ""{
		scale, err := strconv.ParseFloat(scaleStr, 64)
		if scale <= 0 || scale > 1.0 {
			err = errors.New("scale out size.")
		}
		if err != nil{
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	
	img, format, err := image.Decode(bytes.NewReader(src))


}
