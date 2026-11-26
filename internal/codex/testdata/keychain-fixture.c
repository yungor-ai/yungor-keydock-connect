#include <Security/Security.h>
#include <stdio.h>
#include <string.h>

int main(int argc, char **argv) {
    if (argc != 3) return 2;
    SecKeychainRef keychain = NULL;
    OSStatus status;
    if (strcmp(argv[1], "create") == 0) {
        // Keep the fixture private and unlocked in this login session. This
        // filename is never the user's login keychain or System keychain.
        static const char password[] = "keydock-disposable-test-fixture";
        status = SecKeychainCreate(argv[2], sizeof(password) - 1, password,
                                   false, NULL, &keychain);
    } else if (strcmp(argv[1], "delete") == 0) {
        status = SecKeychainOpen(argv[2], &keychain);
        if (status == errSecSuccess) status = SecKeychainDelete(keychain);
    } else {
        return 2;
    }
    if (keychain) CFRelease(keychain);
    if (status == errSecSuccess) return 0;
    fprintf(stderr, "Keychain fixture OSStatus %d\n", (int)status);
    return 1;
}
