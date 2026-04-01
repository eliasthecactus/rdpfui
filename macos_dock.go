//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>
#import <Foundation/Foundation.h>

void setMacAppIcon(const void *data, long len) {
    NSData *imageData = [NSData dataWithBytes:data length:(NSUInteger)len];
    NSImage *image = [[NSImage alloc] initWithData:imageData];
    if (image != nil) {
        [NSApp setApplicationIconImage:image];
    }
}

void hideMacDockIcon() {
    [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
}
*/
import "C"
import "unsafe"

func setMacAppIcon(iconData []byte) error {
	if len(iconData) == 0 {
		return nil
	}
	C.setMacAppIcon(unsafe.Pointer(&iconData[0]), C.long(len(iconData)))
	return nil
}

func hideMacDockIcon() {
	C.hideMacDockIcon()
}
