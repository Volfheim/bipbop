# Keep the VPN Service and its native bridge methods
-keep class com.volfheim.bipbop.BipBopVpnService {
    @androidx.annotation.Keep <methods>;
    void onVpnStatusChanged(java.lang.String);
    void onNativeLog(java.lang.String, java.lang.String);
}

# Keep the JNI bridge methods themselves if needed (though they are usually kept by default)
-keepclasseswithmembernames class * {
    native <methods>;
}

# General Flutter ProGuard rules are usually included by default, 
# but we add this to be super safe for our JNI calls.
