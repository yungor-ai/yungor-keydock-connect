// Exercise the same field merges as the launch daemon against in-memory
// dictionaries. This executable never opens the system preferences.
#import "../settings_darwin.m"

int main(void) {
    @autoreleasepool {
        NSDictionary *prior = @{
            @"HTTPEnable": @0, @"HTTPProxy": @"office.example", @"HTTPPort": @8080,
            @"SOCKSEnable": @1, @"SOCKSProxy": @"custom.example", @"SOCKSPort": @8000,
            @"ProxyAutoConfigEnable": @1, @"ProxyAutoConfigURLString": @"https://example.invalid/proxy.pac",
            @"ExceptionsList": @[@"*.office.test"]
        };
        NSDictionary *owned = ownedFields(17890);
        NSDictionary *receipt = priorFields(prior, owned);
        NSMutableDictionary *current = [prior mutableCopy];
        [current addEntriesFromDictionary:owned];
        if (!matchesOwned(current, owned)) return 5;
        if (![restoredFields(current, receipt, owned) isEqual:prior]) return 1;
        current[@"HTTPSProxy"] = @"new.example";
        current[@"HTTPSPort"] = @9000;
        current[@"ExceptionsList"] = @[@"*.new.test"];
        if (matchesOwned(current, owned)) return 6;
        NSDictionary *next = restoredFields(current, receipt, owned);
        if (![next[@"HTTPSProxy"] isEqual:@"new.example"] || ![next[@"HTTPSPort"] isEqual:@9000]
            || ![next[@"HTTPSEnable"] isEqual:@1] || ![next[@"ExceptionsList"] isEqual:current[@"ExceptionsList"]]) return 2;
        if (![next[@"HTTPProxy"] isEqual:prior[@"HTTPProxy"]] || ![next[@"SOCKSEnable"] isEqual:@1]) return 3;
        if (![restoredFields(next, receipt, owned) isEqual:next]) return 4;
        return 0;
    }
}
