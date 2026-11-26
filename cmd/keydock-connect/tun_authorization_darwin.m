//go:build darwin && arm64 && cgo

#import <ServiceManagement/ServiceManagement.h>

static const char *requestTUNApproval(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [SMAppService openSystemSettingsLoginItems];
    });
    return "请在系统设置的后台项目中允许 KeyDock Connect，再返回启用 TUN。";
}

const char *kdAuthorizeDarwinTUN(void) {
    @autoreleasepool {
        NSString *name = @"ai.yungor.keydock-connect.tun.plist";
        NSURL *plist = [NSBundle.mainBundle.bundleURL URLByAppendingPathComponent:@"Contents/Library/LaunchDaemons"];
        if (![NSFileManager.defaultManager fileExistsAtPath:[plist URLByAppendingPathComponent:name].path]) {
            return "缺少 TUN 后台组件，请重新安装 KeyDock Connect。";
        }

        SMAppService *service = [SMAppService daemonServiceWithPlistName:name];
        SMAppServiceStatus status = service.status;
        if (status == SMAppServiceStatusEnabled) return NULL;
        if (status == SMAppServiceStatusRequiresApproval) return requestTUNApproval();

        BOOL registered = [service registerAndReturnError:NULL];
        status = service.status;
        if (status == SMAppServiceStatusEnabled) return NULL;
        if (status == SMAppServiceStatusRequiresApproval || registered) return requestTUNApproval();
        return "无法注册 TUN 后台组件，请从「应用程序」打开已签名和公证的 KeyDock Connect 后重试。";
    }
}
