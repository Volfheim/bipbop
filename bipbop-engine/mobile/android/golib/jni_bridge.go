package main

/*
#include <jni.h>
#include <stdlib.h>
#include <android/log.h>

#define LOG_TAG "JNI_BIPBOP"
#define LOGI(...) __android_log_print(ANDROID_LOG_INFO, LOG_TAG, __VA_ARGS__)
#define LOGE(...) __android_log_print(ANDROID_LOG_ERROR, LOG_TAG, __VA_ARGS__)

static JavaVM* g_jvm = NULL;
static jobject g_service = NULL;
static jmethodID g_onStatusM = NULL;
static jmethodID g_onLogM = NULL;

static void cleanup_jni(JNIEnv* env) {
    if (g_jvm != NULL && g_service != NULL) {
        LOGI("Cleaning up JNI GlobalRef...");
        (*env)->DeleteGlobalRef(env, g_service);
        g_service = NULL;
    }
    g_onStatusM = NULL;
    g_onLogM = NULL;
}

static void init_jni(JNIEnv* env, jobject service) {
    LOGI("init_jni called");
    cleanup_jni(env);
    (*env)->GetJavaVM(env, &g_jvm);
    g_service = (*env)->NewGlobalRef(env, service);
    
    jclass cls = (*env)->GetObjectClass(env, service);
    if (!cls) {
        LOGE("Failed to get service class!");
        return;
    }
    
    g_onStatusM = (*env)->GetMethodID(env, cls, "onVpnStatusChanged", "(Ljava/lang/String;)V");
    g_onLogM = (*env)->GetMethodID(env, cls, "onNativeLog", "(Ljava/lang/String;Ljava/lang/String;)V");
    
    if (!g_onStatusM || !g_onLogM) {
        LOGE("Failed to find native callback methods! Check ProGuard rules.");
    } else {
        LOGI("JNI callbacks initialized successully");
    }
    
    (*env)->DeleteLocalRef(env, cls);
}

static void call_on_status(const char* status) {
    if (g_jvm == NULL || g_service == NULL || g_onStatusM == NULL) return;
    JNIEnv* env;
    jint res = (*g_jvm)->AttachCurrentThreadAsDaemon(g_jvm, (void**)&env, NULL);
    if (res != JNI_OK) return;
    jstring jStatus = (*env)->NewStringUTF(env, status);
    (*env)->CallVoidMethod(env, g_service, g_onStatusM, jStatus);
    (*env)->DeleteLocalRef(env, jStatus);
}

static void call_on_log(const char* level, const char* msg) {
    if (g_jvm == NULL || g_service == NULL || g_onLogM == NULL) return;
    JNIEnv* env;
    jint res = (*g_jvm)->AttachCurrentThreadAsDaemon(g_jvm, (void**)&env, NULL);
    if (res != JNI_OK) return;
    jstring jLevel = (*env)->NewStringUTF(env, level);
    jstring jMsg = (*env)->NewStringUTF(env, msg);
    (*env)->CallVoidMethod(env, g_service, g_onLogM, jLevel, jMsg);
    (*env)->DeleteLocalRef(env, jLevel);
    (*env)->DeleteLocalRef(env, jMsg);
}

static const char* env_GetStringUTFChars(JNIEnv* env, jstring str, jboolean* isCopy) {
    return (*env)->GetStringUTFChars(env, str, isCopy);
}

static void env_ReleaseStringUTFChars(JNIEnv* env, jstring str, const char* chars) {
    (*env)->ReleaseStringUTFChars(env, str, chars);
}
*/
import "C"
import (
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	jniMu      sync.Mutex
	jniStopped atomic.Bool
)

type jniListener struct{}

func (jniListener) OnStatusChanged(status string) {
	if jniStopped.Load() { return }
	jniMu.Lock()
	defer jniMu.Unlock()
	cs := C.CString(status)
	defer C.free(unsafe.Pointer(cs))
	C.call_on_status(cs)
}

func (jniListener) OnLog(level, msg string) {
	if jniStopped.Load() { return }
	jniMu.Lock()
	defer jniMu.Unlock()
	cl := C.CString(level)
	cm := C.CString(msg)
	defer C.free(unsafe.Pointer(cl))
	defer C.free(unsafe.Pointer(cm))
	C.call_on_log(cl, cm)
}

func (jniListener) OnStatsUpdate(tx, rx int64) {}
func (jniListener) OnTurnInfo(url string)      {}

//export Java_com_volfheim_bipbop_BipBopVpnService_startVpnNative
func Java_com_volfheim_bipbop_BipBopVpnService_startVpnNative(env *C.JNIEnv, service C.jobject, smartKey C.jstring, tunFd C.jint, mtu C.jint, dns C.jstring) C.jint {
	jniMu.Lock()
	C.init_jni(env, service)
	jniMu.Unlock()

	SetListener(jniListener{})

	sk_ptr := C.env_GetStringUTFChars(env, smartKey, nil)
	sk := C.GoString((*C.char)(unsafe.Pointer(sk_ptr)))
	C.env_ReleaseStringUTFChars(env, smartKey, sk_ptr)

	dn_ptr := C.env_GetStringUTFChars(env, dns, nil)
	dn := C.GoString((*C.char)(unsafe.Pointer(dn_ptr)))
	C.env_ReleaseStringUTFChars(env, dns, dn_ptr)

	err := Start(sk, int(tunFd), int(mtu), dn)
	if err != nil {
		return -1
	}
	return 0
}

//export Java_com_volfheim_bipbop_BipBopVpnService_reconnectVpnNative
func Java_com_volfheim_bipbop_BipBopVpnService_reconnectVpnNative(env *C.JNIEnv, service C.jobject) {
	Reconnect()
}

//export Java_com_volfheim_bipbop_BipBopVpnService_stopVpnNative
func Java_com_volfheim_bipbop_BipBopVpnService_stopVpnNative(env *C.JNIEnv, service C.jobject) {
	jniStopped.Store(true)
	Stop()
	
	jniMu.Lock()
	C.cleanup_jni(env)
	jniMu.Unlock()
}
