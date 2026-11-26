//go:build darwin && arm64 && cgo

#import <AppKit/AppKit.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>
#include <stdlib.h>
#include "_cgo_export.h"
#include "window_darwin.h"

static NSString *const reopenNotification = @"ai.yungor.keydock-connect.reopen";

@interface KDWindowController : NSObject <NSApplicationDelegate, NSWindowDelegate>
@property NSWindow *window;
@property NSStatusItem *statusItem;
@property NSTextField *server;
@property NSSecureTextField *deviceKey;
@property NSTextField *device;
@property NSTextField *proxyTitle;
@property NSTextField *proxyStatus;
@property NSTextField *proxyUpdated;
@property NSTextField *notice;
@property NSTextField *hint;
@property NSView *troubleshooting;
@property NSStackView *body;
@property NSArray<NSTextField *> *checkStatus;
@property NSArray<NSTextField *> *checkDetails;
@property NSButton *connectButton;
@property NSButton *softwareButton;
@property NSButton *updateButton;
@property NSButton *networkCheckButton;
@property NSPopUpButton *proxyModePicker;
@property NSButton *proxyButton;
@property NSButton *addressCopyButton;
@property NSButton *restoreButton;
@property NSButton *exportButton;
@property NSString *proxyAddress;
@property int proxyMode;
@property BOOL busy;
@property BOOL quitting;
@property BOOL installingUpdate;
@property BOOL restorable;
@property BOOL subscription;
@end

static KDWindowController *ui;
static NSString *str(const char *buffer) {
    return buffer ? ([NSString stringWithUTF8String:buffer] ?: @"") : @"";
}

static NSTextField *label(NSString *text, CGFloat size, NSFontWeight weight, NSColor *color) {
    NSTextField *field = [NSTextField labelWithString:text];
    field.font = [NSFont systemFontOfSize:size weight:weight];
    field.textColor = color;
    return field;
}

static void singleLine(NSTextField *field) {
    field.lineBreakMode = NSLineBreakByTruncatingTail;
    [field setContentCompressionResistancePriority:NSLayoutPriorityDefaultLow
                                    forOrientation:NSLayoutConstraintOrientationHorizontal];
}

static void setText(NSTextField *field, NSString *text) {
    field.stringValue = text;
    field.toolTip = text.length ? text : nil;
}

static NSStackView *stack(NSArray<NSView *> *views, NSUserInterfaceLayoutOrientation orientation, CGFloat spacing) {
    NSStackView *result = [NSStackView stackViewWithViews:views];
    result.orientation = orientation;
    result.alignment = orientation == NSUserInterfaceLayoutOrientationVertical ? NSLayoutAttributeLeading : NSLayoutAttributeCenterY;
    result.spacing = spacing;
    return result;
}

static NSColor *appearanceColor(NSUInteger light, NSUInteger dark, NSColor *accessible) {
    return [NSColor colorWithName:nil dynamicProvider:^NSColor *(NSAppearance *appearance) {
        if ([appearance.name containsString:@"HighContrast"]) return accessible;
        NSString *style = [appearance bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
        NSUInteger rgb = [style isEqualToString:NSAppearanceNameDarkAqua] ? dark : light;
        return [NSColor colorWithSRGBRed:((rgb >> 16) & 0xff) / 255.0 green:((rgb >> 8) & 0xff) / 255.0
                                  blue:(rgb & 0xff) / 255.0 alpha:1];
    }];
}

static NSColor *ink(void) { return appearanceColor(0x193137, 0xe6f1f2, NSColor.labelColor); }
static NSColor *muted(void) { return appearanceColor(0x4b626c, 0xb6cacf, NSColor.secondaryLabelColor); }
static NSColor *accent(void) { return appearanceColor(0x00666f, 0x71d2d5, NSColor.labelColor); }
static NSColor *success(void) { return appearanceColor(0x0f664d, 0x82d9b0, NSColor.systemGreenColor); }
static NSColor *danger(void) { return appearanceColor(0xa83034, 0xff9599, NSColor.systemRedColor); }

@interface KDSurface : NSView
@property NSColor *fill;
@end

@implementation KDSurface
- (BOOL)isFlipped { return YES; }
- (void)drawRect:(NSRect)dirty {
    [self.fill setFill];
    NSRectFill(NSIntersectionRect(dirty, self.bounds));
}
- (void)viewDidChangeEffectiveAppearance {
    [super viewDidChangeEffectiveAppearance];
    self.needsDisplay = YES;
}
@end

static void pin(NSView *view, NSView *parent, NSEdgeInsets insets) {
    view.translatesAutoresizingMaskIntoConstraints = NO;
    [parent addSubview:view];
    [NSLayoutConstraint activateConstraints:@[
        [view.leadingAnchor constraintEqualToAnchor:parent.leadingAnchor constant:insets.left],
        [view.trailingAnchor constraintEqualToAnchor:parent.trailingAnchor constant:-insets.right],
        [view.topAnchor constraintEqualToAnchor:parent.topAnchor constant:insets.top],
        [view.bottomAnchor constraintEqualToAnchor:parent.bottomAnchor constant:-insets.bottom]
    ]];
}

static KDSurface *surface(NSView *content, NSColor *color, NSEdgeInsets insets) {
    KDSurface *view = [KDSurface new];
    view.fill = color;
    if (content) pin(content, view, insets);
    return view;
}

static NSStackView *column(NSArray<NSView *> *views, CGFloat spacing) {
    NSStackView *result = stack(views, NSUserInterfaceLayoutOrientationVertical, spacing);
    result.detachesHiddenViews = NO;
    for (NSView *view in views) {
        [view.widthAnchor constraintEqualToAnchor:result.widthAnchor].active = YES;
    }
    return result;
}

static NSTextField *heading(NSString *text) { return label(text, 16, NSFontWeightSemibold, ink()); }

static NSArray<NSString *> *checkDescriptions(void) {
    return @[@"从服务器获取设备配置", @"写入本机 Codex 配置", @"发送模型测试请求",
             @"通过 SS 节点测试 HTTPS 请求", @"检测 google.com 的访问状态", @"检测公网 IPv4 并与 KeyDock 服务器比较"];
}

@implementation KDWindowController

- (NSButton *)button:(NSString *)title action:(SEL)action {
    NSButton *button = [NSButton buttonWithTitle:title target:self action:action];
    button.bezelStyle = NSBezelStyleRounded;
    return button;
}

- (void)installMenu {
    NSMenu *bar = [NSMenu new];
    NSMenuItem *appItem = [NSMenuItem new];
    [bar addItem:appItem];
    appItem.submenu = [[NSMenu alloc] initWithTitle:@"KeyDock Connect"];
    [appItem.submenu addItemWithTitle:@"退出 KeyDock Connect" action:@selector(terminate:) keyEquivalent:@"q"];

    NSMenuItem *editItem = [NSMenuItem new];
    [bar addItem:editItem];
    editItem.submenu = [[NSMenu alloc] initWithTitle:@"编辑"];
    [editItem.submenu addItemWithTitle:@"剪切" action:@selector(cut:) keyEquivalent:@"x"];
    [editItem.submenu addItemWithTitle:@"复制" action:@selector(copy:) keyEquivalent:@"c"];
    [editItem.submenu addItemWithTitle:@"粘贴" action:@selector(paste:) keyEquivalent:@"v"];
    [editItem.submenu addItemWithTitle:@"全选" action:@selector(selectAll:) keyEquivalent:@"a"];
    NSApp.mainMenu = bar;

    self.statusItem = [NSStatusBar.systemStatusBar statusItemWithLength:NSVariableStatusItemLength];
    NSStatusBarButton *statusButton = self.statusItem.button;
    NSImage *keyIcon = [NSImage imageWithSystemSymbolName:@"key.fill" accessibilityDescription:@"KeyDock Connect"];
    if (keyIcon) {
        keyIcon.template = YES;
        statusButton.image = keyIcon;
        statusButton.imagePosition = NSImageOnly;
    } else {
        statusButton.title = @"KeyDock";
    }
    statusButton.toolTip = @"KeyDock Connect";
    statusButton.accessibilityLabel = @"KeyDock Connect";

    NSMenu *statusMenu = [NSMenu new];
    NSMenuItem *show = [statusMenu addItemWithTitle:@"打开 KeyDock Connect"
                                            action:@selector(openFromStatusItem:) keyEquivalent:@""];
    show.target = self;
    [statusMenu addItem:NSMenuItem.separatorItem];
    NSMenuItem *quit = [statusMenu addItemWithTitle:@"退出接入助手" action:@selector(terminate:) keyEquivalent:@""];
    quit.target = NSApp;
    self.statusItem.menu = statusMenu;
}

- (void)openFromStatusItem:(NSMenuItem *)sender {
    [self reveal];
}

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    [self installMenu];
    self.window = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 680, 620)
                                              styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                                                        NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
                                                backing:NSBackingStoreBuffered defer:NO];
    NSString *version = [NSBundle.mainBundle objectForInfoDictionaryKey:@"CFBundleShortVersionString"];
    self.window.title = version.length ? [@"KeyDock Connect v" stringByAppendingString:version] : @"KeyDock Connect";
    self.window.contentMinSize = NSMakeSize(540, 500);
    self.window.delegate = self;
    self.window.releasedWhenClosed = NO;

    NSColor *primary = ink(), *secondary = muted();
    NSColor *page = appearanceColor(0xf0f4f5, 0x192124, NSColor.windowBackgroundColor);
    NSColor *card = appearanceColor(0xffffff, 0x252e31, NSColor.controlBackgroundColor);
    NSColor *teal = appearanceColor(0xdef1ef, 0x21403e, NSColor.controlBackgroundColor);
    NSColor *proxyFill = appearanceColor(0xe5f1f4, 0x233c41, NSColor.controlBackgroundColor);
    NSColor *badge = appearanceColor(0xf0f5f6, 0x314247, NSColor.windowBackgroundColor);
    NSColor *warning = appearanceColor(0xfff6e0, 0x3e3625, NSColor.controlBackgroundColor);

    NSImage *appIcon = [NSImage imageNamed:NSImageNameApplicationIcon];
    NSImageView *icon = [NSImageView imageViewWithImage:appIcon];
    icon.imageScaling = NSImageScaleProportionallyUpOrDown;
    [icon setAccessibilityElement:NO];
    [icon.widthAnchor constraintEqualToConstant:44].active = YES;
    [icon.heightAnchor constraintEqualToConstant:44].active = YES;
    NSTextField *title = label(@"设备接入", 24, NSFontWeightSemibold, primary);
    self.device = label(@"输入管理员提供的服务器地址和设备密钥。", 13, NSFontWeightRegular, secondary);
    singleLine(self.device);
    NSStackView *identity = stack(@[title, self.device], NSUserInterfaceLayoutOrientationVertical, 2);
    KDSurface *header = surface(stack(@[icon, identity, [NSView new]],
                                     NSUserInterfaceLayoutOrientationHorizontal, 14), teal, NSEdgeInsetsMake(14, 22, 14, 22));
    [header.heightAnchor constraintGreaterThanOrEqualToConstant:78].active = YES;

    self.server = [NSTextField textFieldWithString:@""];
    self.server.placeholderString = @"https://keydock.example.com";
    self.server.bezelStyle = NSTextFieldSquareBezel;
    self.server.accessibilityLabel = @"服务器地址";
    self.server.accessibilityIdentifier = @"keydock.server";
    self.deviceKey = [NSSecureTextField new];
    self.deviceKey.placeholderString = @"管理员签发的设备密钥";
    self.deviceKey.bezelStyle = NSTextFieldSquareBezel;
    self.deviceKey.accessibilityLabel = @"Device Key";
    for (NSTextField *input in @[self.server, self.deviceKey]) {
        input.font = [NSFont systemFontOfSize:13];
        [input setContentCompressionResistancePriority:NSLayoutPriorityDefaultLow forOrientation:NSLayoutConstraintOrientationHorizontal];
        [input.heightAnchor constraintEqualToConstant:32].active = YES;
    }
    self.connectButton = [self button:@"连接并测试" action:@selector(connect:)];
    self.connectButton.keyEquivalent = @"\r";
    [self.connectButton.widthAnchor constraintEqualToConstant:110].active = YES;
    NSStackView *serverField = column(@[label(@"服务器地址", 13, NSFontWeightRegular, secondary), self.server], 4);
    NSStackView *keyField = column(@[label(@"Device Key", 13, NSFontWeightRegular, secondary), self.deviceKey], 4);
    NSStackView *form = stack(@[serverField, keyField, self.connectButton], NSUserInterfaceLayoutOrientationHorizontal, 10);
    form.distribution = NSStackViewDistributionFill;
    form.alignment = NSLayoutAttributeBottom;
    [serverField.widthAnchor constraintEqualToAnchor:keyField.widthAnchor].active = YES;
    self.server.nextKeyView = self.deviceKey;
    self.deviceKey.nextKeyView = self.connectButton;
    self.softwareButton = [self button:@"软件下载" action:@selector(openSoftware:)];
    self.softwareButton.accessibilityIdentifier = @"keydock.software";
    self.updateButton = [self button:@"检查更新" action:@selector(checkUpdate:)];
    self.updateButton.accessibilityIdentifier = @"keydock.check-update";
    NSStackView *connectionHeader = stack(@[heading(@"连接设置"), [NSView new], self.softwareButton, self.updateButton],
                                          NSUserInterfaceLayoutOrientationHorizontal, 8);
    NSEdgeInsets sectionInsets = NSEdgeInsetsMake(12, 18, 14, 18);
    KDSurface *connection = surface(column(@[connectionHeader, form], 8), card, sectionInsets);

    self.networkCheckButton = [self button:@"重新检测网络" action:@selector(recheckNetwork:)];
    self.exportButton = [self button:@"导出诊断日志" action:@selector(exportLog:)];
    NSStackView *checkHeader = stack(@[heading(@"连接检查"), [NSView new], self.networkCheckButton, self.exportButton],
                                     NSUserInterfaceLayoutOrientationHorizontal, 8);
    NSMutableArray<NSArray<NSView *> *> *results = [NSMutableArray new];
    NSMutableArray<NSTextField *> *statuses = [NSMutableArray new];
    NSMutableArray<NSTextField *> *details = [NSMutableArray new];
    NSArray<NSString *> *stages = @[@"设备授权", @"Codex 配置", @"模型连通", @"代理连通", @"Google 连通", @"出口 IPv4"];
    NSArray<NSString *> *descriptions = checkDescriptions();
    for (NSUInteger row = 0; row < stages.count; row++) {
        NSTextField *status = label(@"待检查", 12, NSFontWeightMedium, secondary);
        status.alignment = NSTextAlignmentCenter;
        KDSurface *statusBadge = surface(status, badge, NSEdgeInsetsMake(3, 0, 3, 0));
        [statusBadge.widthAnchor constraintEqualToConstant:60].active = YES;
        NSTextField *detail = label(descriptions[row], 13, NSFontWeightRegular, secondary);
        singleLine(detail);
        [statuses addObject:status];
        [details addObject:detail];
        [results addObject:@[label(stages[row], 13, NSFontWeightRegular, primary), statusBadge, detail]];
    }
    self.checkStatus = statuses;
    self.checkDetails = details;
    NSGridView *checks = [NSGridView gridViewWithViews:results];
    checks.columnSpacing = 8;
    checks.rowSpacing = 8;
    [checks columnAtIndex:0].width = 88;
    [checks columnAtIndex:1].width = 60;
    [checks columnAtIndex:2].xPlacement = NSGridCellPlacementFill;
    checks.rowAlignment = NSGridRowAlignmentNone;
    checks.yPlacement = NSGridCellPlacementCenter;
    KDSurface *checkCard = surface(column(@[checkHeader, checks], 8), card, sectionInsets);

    self.hint = [NSTextField wrappingLabelWithString:@""];
    self.hint.font = [NSFont systemFontOfSize:12];
    self.hint.textColor = primary;
    [self.hint setContentCompressionResistancePriority:NSLayoutPriorityDefaultLow forOrientation:NSLayoutConstraintOrientationHorizontal];
    self.troubleshooting = surface(column(@[heading(@"代理故障排查"), self.hint], 4), warning, NSEdgeInsetsMake(8, 18, 10, 18));

    self.proxyTitle = heading(@"本地代理未启用");
    self.proxyModePicker = [[NSPopUpButton alloc] initWithFrame:NSZeroRect pullsDown:NO];
    [self.proxyModePicker addItemsWithTitles:@[@"本地代理", @"TUN（虚拟网卡）"]];
    [self.proxyModePicker itemAtIndex:0].tag = KD_PROXY_LOCAL;
    [self.proxyModePicker itemAtIndex:1].tag = KD_PROXY_TUN;
    self.proxyModePicker.accessibilityLabel = @"代理模式";
    self.proxyModePicker.target = self;
    self.proxyModePicker.action = @selector(proxyModeChanged:);
    [self.proxyModePicker.widthAnchor constraintGreaterThanOrEqualToConstant:126].active = YES;
    self.proxyButton = [self button:@"启用本地代理" action:@selector(toggleProxy:)];
    self.addressCopyButton = [self button:@"复制地址" action:@selector(copyAddress:)];
    self.proxyStatus = label(@"连接设备并获取 SS 订阅后，可在此启用。", 13, NSFontWeightRegular, secondary);
    singleLine(self.proxyStatus);
    self.proxyUpdated = label(@"", 12, NSFontWeightSemibold, primary);
    singleLine(self.proxyUpdated);
    NSStackView *proxyHeader = stack(@[self.proxyTitle, [NSView new], self.addressCopyButton,
                                      self.proxyModePicker, self.proxyButton],
                                     NSUserInterfaceLayoutOrientationHorizontal, 8);
    KDSurface *proxyCard = surface(column(@[proxyHeader, self.proxyUpdated, self.proxyStatus], 4),
                                   proxyFill, NSEdgeInsetsMake(10, 18, 11, 18));

    self.body = column(@[connection, checkCard, proxyCard], 10);
    KDSurface *document = surface(self.body, page, NSEdgeInsetsMake(12, 16, 12, 16));
    NSScrollView *scroll = [NSScrollView new];
    scroll.drawsBackground = NO;
    scroll.hasVerticalScroller = YES;
    scroll.autohidesScrollers = YES;
    scroll.scrollerStyle = NSScrollerStyleOverlay;
    scroll.documentView = document;
    document.translatesAutoresizingMaskIntoConstraints = NO;
    [NSLayoutConstraint activateConstraints:@[
        [document.topAnchor constraintEqualToAnchor:scroll.contentView.topAnchor],
        [document.leadingAnchor constraintEqualToAnchor:scroll.contentView.leadingAnchor],
        [document.widthAnchor constraintEqualToAnchor:scroll.contentView.widthAnchor]
    ]];

    self.notice = label(@"关闭窗口后可从菜单栏重新打开。", 12, NSFontWeightRegular, secondary);
    self.notice.accessibilityIdentifier = @"keydock.notice";
    singleLine(self.notice);
    self.restoreButton = [self button:@"恢复原 Codex 配置" action:@selector(restore:)];
    NSButton *quit = [self button:@"退出接入助手" action:@selector(terminate:)];
    quit.target = NSApp;
    KDSurface *footer = surface(stack(@[self.notice, [NSView new], self.restoreButton, quit],
                                     NSUserInterfaceLayoutOrientationHorizontal, 8), card, NSEdgeInsetsMake(8, 20, 8, 16));

    KDSurface *root = surface(nil, page, NSEdgeInsetsMake(0, 0, 0, 0));
    self.window.contentView = root;
    for (NSView *view in @[header, scroll, footer]) {
        view.translatesAutoresizingMaskIntoConstraints = NO;
        [root addSubview:view];
        [view.leadingAnchor constraintEqualToAnchor:root.leadingAnchor].active = YES;
        [view.trailingAnchor constraintEqualToAnchor:root.trailingAnchor].active = YES;
    }
    [NSLayoutConstraint activateConstraints:@[
        [header.topAnchor constraintEqualToAnchor:root.topAnchor],
        [scroll.topAnchor constraintEqualToAnchor:header.bottomAnchor],
        [scroll.bottomAnchor constraintEqualToAnchor:footer.topAnchor],
        [footer.bottomAnchor constraintEqualToAnchor:root.bottomAnchor]
    ]];

    self.proxyAddress = @"";
    self.busy = YES;
    [self updateButtons];
    [self.window center];
    [self reveal];
    [[NSDistributedNotificationCenter defaultCenter] addObserver:self selector:@selector(reopen:)
                                                           name:reopenNotification object:nil
                                             suspensionBehavior:NSNotificationSuspensionBehaviorDeliverImmediately];
    [NSTimer scheduledTimerWithTimeInterval:3 target:self selector:@selector(pollProxy:) userInfo:nil repeats:YES];
    kdMacReady();
}

- (void)reveal {
    if (!self.quitting) {
        if (NSApp.activationPolicy != NSApplicationActivationPolicyRegular) {
            [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        }
        if (self.window.miniaturized) [self.window deminiaturize:nil];
        [self.window makeKeyAndOrderFront:nil];
        [NSApp activateIgnoringOtherApps:YES];
    }
}

- (BOOL)applicationShouldHandleReopen:(NSApplication *)sender hasVisibleWindows:(BOOL)visible {
    [self reveal];
    return YES;
}

- (void)reopen:(NSNotification *)notification {
    dispatch_async(dispatch_get_main_queue(), ^{ [self reveal]; });
}

- (BOOL)windowShouldClose:(NSWindow *)sender {
    [self.window makeFirstResponder:nil];
    self.deviceKey.stringValue = @"";
    [sender orderOut:nil];
    if (self.statusItem.button) [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    return NO;
}

- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)sender {
    if (!self.quitting) {
        self.quitting = YES;
        [self updateButtons];
        kdMacQuit(self.installingUpdate ? 1 : 0);
    }
    return NSTerminateLater;
}

- (void)updateButtons {
    BOOL idle = !self.busy && !self.quitting;
    self.connectButton.enabled = idle;
    self.softwareButton.enabled = idle;
    self.updateButton.enabled = idle;
    self.networkCheckButton.enabled = !self.quitting;
    self.server.enabled = idle;
    self.deviceKey.enabled = idle;
    self.restoreButton.enabled = idle && self.restorable;
    self.exportButton.enabled = idle;
    [self renderProxy];
}

- (int)selectedProxyMode {
    return self.proxyModePicker.selectedTag == KD_PROXY_TUN ? KD_PROXY_TUN : KD_PROXY_LOCAL;
}

- (void)renderProxy {
    int selected = [self selectedProxyMode];
    BOOL active = self.proxyMode != KD_PROXY_INACTIVE;
    BOOL idle = !self.busy && !self.quitting;
    BOOL tun = (active ? self.proxyMode : selected) == KD_PROXY_TUN;
    self.proxyModePicker.enabled = idle;
    self.proxyButton.enabled = idle && (selected == self.proxyMode || self.subscription);
    self.addressCopyButton.hidden = tun;
    self.addressCopyButton.enabled = idle && self.proxyMode == KD_PROXY_LOCAL && self.proxyAddress.length;

    NSString *title = active ? @"本地代理已启用" : @"本地代理未启用";
    NSString *action = !active ? @"启用" : (selected == self.proxyMode ? @"关闭" : @"切换至");
    NSString *detail = @"连接设备并获取 SS 订阅后，可在此启用。";
    if (tun) {
        title = active ? @"TUN 已启用" : @"TUN 未启用";
        if (active) {
            detail = @"流量正通过虚拟网卡按订阅规则分流。";
        } else if (self.subscription) {
            detail = @"通过虚拟网卡分流。首次启用时需在 macOS 中批准后台项目。";
        }
    } else if (active && self.proxyAddress.length) {
        detail = [@"HTTP / SOCKS5  " stringByAppendingString:self.proxyAddress];
    } else if (self.subscription) {
        detail = @"启用后，可将地址填入支持 HTTP 或 SOCKS5 的应用。";
    }
    self.proxyButton.title = [action stringByAppendingString:selected == KD_PROXY_TUN ? @" TUN" : @"本地代理"];
    self.proxyTitle.textColor = active ? success() : ink();
    setText(self.proxyTitle, title);
    setText(self.proxyStatus, detail);
}

- (void)updateProxyMode:(int)mode address:(NSString *)address {
    self.proxyMode = mode;
    self.proxyAddress = mode == KD_PROXY_LOCAL ? address : @"";
    [self updateButtons];
}

- (void)pollProxy:(NSTimer *)timer {
    if (self.busy || self.quitting || self.proxyMode == KD_PROXY_INACTIVE) return;
    int mode = kdMacProxyMode();
    char *value = mode == KD_PROXY_LOCAL ? kdMacProxyAddress() : NULL;
    NSString *address = str(value);
    free(value);
    if (self.proxyMode == mode && [self.proxyAddress isEqualToString:address]) return;
    int previousMode = self.proxyMode;
    [self updateProxyMode:mode address:address];
    if (mode == KD_PROXY_INACTIVE) {
        NSString *notice = previousMode == KD_PROXY_TUN ? @"TUN 已停止。可重新启用或导出诊断日志。" :
                                                           @"本地代理已停止。可重新启用或导出诊断日志。";
        [self showNotice:notice failure:YES];
    }
}

- (void)showNotice:(NSString *)message failure:(BOOL)failure {
    setText(self.notice, message);
    self.notice.textColor = failure ? danger() : muted();
}

- (void)showTroubleshooting:(NSString *)tip {
    self.hint.stringValue = tip;
    BOOL shown = [self.body.arrangedSubviews containsObject:self.troubleshooting];
    if (tip.length && !shown) {
        [self.body insertArrangedSubview:self.troubleshooting atIndex:2];
        [self.troubleshooting.widthAnchor constraintEqualToAnchor:self.body.widthAnchor].active = YES;
    } else if (!tip.length && shown) {
        [self.body removeArrangedSubview:self.troubleshooting];
        [self.troubleshooting removeFromSuperview];
    }
}

- (void)runAction:(int)action first:(NSString *)first second:(NSString *)second {
    if (self.busy || self.quitting) return;
    self.busy = YES;
    [self updateButtons];
    [self showNotice:@"正在处理…" failure:NO];
    kdMacAction(action, (char *)first.UTF8String, (char *)second.UTF8String);
}

- (void)connect:(id)sender {
    [self.window makeFirstResponder:nil];
    NSString *key = self.deviceKey.stringValue;
    self.deviceKey.stringValue = @"";
    NSArray<NSString *> *descriptions = checkDescriptions();
    for (NSUInteger row = KD_CHECK_DEVICE; row < KD_CHECK_GOOGLE; row++) {
        NSTextField *status = self.checkStatus[row];
        status.stringValue = @"待检查";
        status.textColor = muted();
        setText(self.checkDetails[row], descriptions[row]);
    }
    [self showTroubleshooting:@""];
    [self runAction:KD_CONNECT first:self.server.stringValue second:key];
}

- (void)openSoftware:(id)sender {
    [self.window makeFirstResponder:nil];
    char *value = kdMacSoftwareURL((char *)self.server.stringValue.UTF8String);
    NSURL *url = value ? [NSURL URLWithString:str(value)] : nil;
    free(value);
    if (!url) {
        [self showNotice:@"请填写有效的服务器地址。" failure:YES];
    } else if (![NSWorkspace.sharedWorkspace openURL:url]) {
        [self showNotice:@"无法打开软件下载页面。" failure:YES];
    }
}

- (void)checkUpdate:(id)sender {
    if (self.busy || self.quitting) return;
    [self.window makeFirstResponder:nil];
    self.busy = YES;
    [self updateButtons];
    [self showNotice:@"正在检查更新…" failure:NO];
    kdMacCheckUpdate((char *)self.server.stringValue.UTF8String);
}

- (void)recheckNetwork:(id)sender { kdMacRecheckNetwork(); }

- (void)proxyModeChanged:(id)sender { [self renderProxy]; }

- (void)toggleProxy:(id)sender {
    int selected = [self selectedProxyMode];
    NSString *toggle = selected == self.proxyMode ? @"off" : @"on";
    NSString *mode = selected == KD_PROXY_TUN ? @"tun" : @"local";
    [self runAction:KD_PROXY first:toggle second:mode];
}

- (void)copyAddress:(id)sender {
    if (self.proxyMode != KD_PROXY_LOCAL || !self.proxyAddress.length) return;
    [NSPasteboard.generalPasteboard clearContents];
    [NSPasteboard.generalPasteboard setString:self.proxyAddress forType:NSPasteboardTypeString];
    [self showNotice:@"已复制本地代理地址，可填入支持 HTTP 或 SOCKS5 的应用。" failure:NO];
}

- (void)restore:(id)sender {
    NSAlert *alert = [NSAlert new];
    alert.messageText = @"恢复 KeyDock 接入前的 Codex 设置？";
    alert.informativeText = @"将撤销接入助手管理的模型和提供商设置，并保留其他 Codex 设置。";
    [alert addButtonWithTitle:@"恢复配置"];
    [alert addButtonWithTitle:@"取消"].keyEquivalent = @"\e";
    [alert beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse response) {
        if (response == NSAlertFirstButtonReturn) [self runAction:KD_RESTORE first:nil second:nil];
    }];
}

- (void)exportLog:(id)sender {
    NSSavePanel *panel = [NSSavePanel savePanel];
    NSDateFormatter *formatter = [NSDateFormatter new];
    formatter.locale = [[NSLocale alloc] initWithLocaleIdentifier:@"en_US_POSIX"];
    formatter.dateFormat = @"yyyyMMdd-HHmmss";
    panel.nameFieldStringValue = [NSString stringWithFormat:@"KeyDockConnect-diagnostics-%@.json", [formatter stringFromDate:NSDate.date]];
    panel.allowedContentTypes = @[UTTypeJSON];
    panel.canCreateDirectories = YES;
    [panel beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse result) {
        if (result == NSModalResponseOK) [self runAction:KD_EXPORT first:panel.URL.path second:nil];
    }];
}
@end

void kdRunWindow(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        ui = [KDWindowController new];
        NSApp.delegate = ui;
        [NSApp run];
    }
}

void kdShowError(const char *message) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        [NSApp activateIgnoringOtherApps:YES];
        NSAlert *alert = [NSAlert new];
        alert.alertStyle = NSAlertStyleCritical;
        alert.messageText = @"KeyDock 接入助手";
        alert.informativeText = str(message);
        [alert runModal];
    }
}

void kdReopenWindow(void) {
    @autoreleasepool {
        [[NSDistributedNotificationCenter defaultCenter] postNotificationName:reopenNotification object:nil
                                                                       userInfo:nil deliverImmediately:YES];
    }
}

void kdSetState(const char *server, const char *device, const char *model,
                int paired, int restorable, int subscription, int proxyMode,
                const char *address, const char *updatedAt, int refreshFailed) {
    @autoreleasepool {
        NSString *initialServer = server ? str(server) : nil;
        NSString *deviceName = str(device), *modelName = str(model), *proxyAddress = str(address), *updated = str(updatedAt);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (ui.quitting) return;
            if (initialServer) {
                ui.server.stringValue = initialServer;
                ui.busy = NO;
            }
            ui.deviceKey.placeholderString = paired ? @"使用原服务器时可留空" : @"管理员签发的设备密钥";
            if (deviceName.length) {
                setText(ui.device, [NSString stringWithFormat:@"%@  ·  %@", deviceName, modelName]);
            } else {
                setText(ui.device, @"输入管理员提供的服务器地址和设备密钥。");
            }
            ui.restorable = restorable;
            ui.subscription = subscription;
            [ui updateProxyMode:proxyMode address:proxyAddress];
            if (initialServer) [ui.window makeFirstResponder:paired ? ui.connectButton : ui.server];
            NSString *updatedText = updated.length ? [@"最近更新时间  " stringByAppendingString:updated] : @"最近更新时间  尚未更新";
            if (!subscription) updatedText = @"";
            if (refreshFailed) updatedText = [updatedText stringByAppendingString:@"  ·  本次刷新失败"];
            setText(ui.proxyUpdated, updatedText);
            ui.proxyUpdated.hidden = !subscription;
        });
    }
}

void kdSetCheck(int row, const char *status, const char *message, const char *hint) {
    @autoreleasepool {
        NSString *outcome = str(status), *detail = str(message), *tip = str(hint);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (ui.quitting) return;
            NSTextField *field = ui.checkStatus[row];
            NSString *passed = @"完成", *failed = @"失败", *skipped = @"跳过";
            if (row == KD_CHECK_GOOGLE) {
                passed = @"可访问";
                failed = @"无法访问";
            } else if (row == KD_CHECK_EGRESS) {
                passed = @"一致";
                skipped = @"未校验";
            }
            BOOL mismatch = row == KD_CHECK_EGRESS && [outcome isEqualToString:@"warning"];
            if ([outcome isEqualToString:@"ok"]) {
                field.stringValue = passed;
                field.textColor = success();
            } else if (mismatch) {
                field.stringValue = @"不一致";
                field.textColor = danger();
            } else if ([outcome isEqualToString:@"failed"]) {
                field.stringValue = failed;
                field.textColor = danger();
            } else {
                BOOL running = [outcome isEqualToString:@"running"];
                field.stringValue = running ? @"进行中" : skipped;
                field.textColor = running ? accent() : muted();
            }
            NSTextField *detailField = ui.checkDetails[row];
            setText(detailField, detail);
            detailField.textColor = mismatch ? danger() : muted();
            if (tip.length) [ui showTroubleshooting:tip];
        });
    }
}

void kdFinishOperation(const char *message, int failed) {
    @autoreleasepool {
        NSString *notice = str(message);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (ui.quitting) return;
            ui.busy = NO;
            [ui updateButtons];
            [ui showNotice:notice failure:failed];
        });
    }
}

void kdOfferUpdate(const char *version) {
    @autoreleasepool {
        NSString *release = str(version);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (ui.quitting) return;
            [ui reveal];
            [ui showNotice:[NSString stringWithFormat:@"发现新版本 v%@。", release] failure:NO];
            NSAlert *alert = [NSAlert new];
            alert.messageText = [NSString stringWithFormat:@"是否更新到 v%@？", release];
            alert.informativeText = @"更新将下载安装包，停止接入助手正在运行的代理，然后重启接入助手。";
            [alert addButtonWithTitle:@"在线更新"];
            [alert addButtonWithTitle:@"取消"].keyEquivalent = @"\e";
            [alert beginSheetModalForWindow:ui.window completionHandler:^(NSModalResponse response) {
                if (ui.quitting) return;
                BOOL accepted = response == NSAlertFirstButtonReturn;
                kdMacUpdateDecision(accepted ? 1 : 0);
                if (accepted) {
                    [ui showNotice:@"正在下载更新…" failure:NO];
                } else {
                    ui.busy = NO;
                    [ui updateButtons];
                    [ui showNotice:@"已取消更新。" failure:NO];
                }
            }];
        });
    }
}

void kdSetUpdateProgress(const char *message) {
    @autoreleasepool {
        NSString *notice = str(message);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (!ui.quitting) [ui showNotice:notice failure:NO];
        });
    }
}

void kdRequestUpdateTermination(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (ui.quitting) return;
        ui.installingUpdate = YES;
        [ui showNotice:@"正在安装更新…" failure:NO];
        // The NSTerminateLater loop must be able to run the reply queued by Go.
        [NSApp performSelector:@selector(terminate:) withObject:nil afterDelay:0];
    });
}

void kdFinishTermination(const char *failure) {
    @autoreleasepool {
        NSString *message = str(failure);
        dispatch_async(dispatch_get_main_queue(), ^{
            if (!message.length) {
                [NSApp replyToApplicationShouldTerminate:YES];
                return;
            }
            kdMacResumeActions();
            ui.installingUpdate = NO;
            ui.quitting = NO;
            ui.busy = NO;
            [ui updateButtons];
            [ui showNotice:message failure:YES];
            [NSApp replyToApplicationShouldTerminate:NO];
            [ui reveal];
        });
    }
}
